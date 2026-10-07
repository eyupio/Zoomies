import { expect, test, type Page } from '@playwright/test';
import {
  browserOverride,
  documentWidth,
  expectNoReload,
  goto,
  plantMarker,
} from './support/fixtures';
test.use(browserOverride);

type Credentials = { host_id: string; agent_token: string };
type Check = {
  id: string;
  title: string;
  tier: 'safe' | 'aggressive' | 'dedicated';
  status: 'ok' | 'warn' | 'skip' | 'error';
  current: string;
  recommended: string;
  rationale: string;
  actionable: boolean;
  optional?: boolean;
  reason?: string;
};

/** Enrol a host the way an agent does, through a join token from the Add a host page. */
async function enrol(page: Page, name: string): Promise<Credentials> {
  await goto(page, '/hosts/new', 'Add a host');
  await page.getByRole('button', { name: 'Get the command' }).click();
  const command = await page
    .getByRole('region', { name: 'Run this on the new host' })
    .locator('pre', { hasText: 'zoomies.sh/install.sh' })
    .innerText();
  const token = /--join-token\s+['"]?(zoojoin_[^'"\s]+)/.exec(command)?.[1];
  const join = await page.request.post('/api/v1/agent/join', {
    data: {
      protocol_version: 1,
      join_token: token,
      name,
      capacity: 1,
      os: 'linux',
      arch: 'amd64',
      version: 'dev',
      backends: [{ kind: 'docker', available: true }],
    },
  });
  expect(join.ok()).toBeTruthy();
  return (await join.json()) as Credentials;
}

async function heartbeat(
  page: Page,
  credentials: Credentials,
  results: Check[],
  reboot = false,
  container = false,
): Promise<void> {
  const response = await page.request.post('/api/v1/agent/heartbeat', {
    headers: { Authorization: `Bearer ${credentials.agent_token}` },
    data: {
      protocol_version: 1,
      doctor: {
        checked_at: new Date().toISOString(),
        os: 'linux',
        distro: 'ubuntu 24.04',
        container,
        reboot_pending: reboot,
        results,
      },
    },
  });
  expect(response.ok()).toBeTruthy();
}

/** Record what the page copies, which a headless browser will not let a test read from the clipboard. */
async function stubClipboard(page: Page): Promise<void> {
  await page.addInitScript(() => {
    Object.defineProperty(navigator, 'clipboard', {
      configurable: true,
      value: {
        writeText: async (value: string) => sessionStorage.setItem('copied', value),
      },
    });
  });
}

function check(
  id: string,
  title: string,
  tier: Check['tier'],
  status: Check['status'],
  extra: Partial<Check> = {},
): Check {
  return {
    id,
    title,
    tier,
    status,
    current: '',
    recommended: '',
    rationale: `${title}: why it matters.`,
    actionable: false,
    ...extra,
  };
}

/** The controller's own count of a host's report, as GET /hosts/{id} sends it. */
async function summaryOf(
  page: Page,
  credentials: Credentials,
): Promise<{ counted: number; warnings: number; errors: number; skipped: number }> {
  const response = await page.request.get(`/api/v1/hosts/${credentials.host_id}`);
  expect(response.ok()).toBeTruthy();
  const host = (await response.json()) as { doctor?: { summary?: Record<string, number> } };
  const summary = host.doctor?.summary;
  expect(summary, 'the controller sends its count beside the results').toBeDefined();
  return summary as { counted: number; warnings: number; errors: number; skipped: number };
}

test('host OS health updates live and stays read-only on the detail page', async ({ page }) => {
  const name = `health-host-${Date.now()}`;
  // Before anything else: the host page is reached by a client-side click, so an
  // init script added later would never run on it.
  await stubClipboard(page);
  const credentials = await enrol(page, name);
  const beat = async (warn: boolean, reboot = false) =>
    heartbeat(
      page,
      credentials,
      [
        check('disk.space', 'Work directory free space', 'safe', warn ? 'warn' : 'ok', {
          current: warn ? '2% free' : '45% free',
          recommended: '10% and 10 GiB free',
          rationale: 'Leave room for new builds.',
        }),
      ],
      reboot,
    );
  try {
    await beat(true);
    await goto(page, '/hosts', 'Hosts');
    const card = page.getByRole('article', { name, exact: true });
    await expect(card.getByRole('link', { name: `Host health for ${name}` })).toContainText(
      '1 warning',
    );
    await plantMarker(page);
    await beat(false);
    await expect(card).toContainText('Health OK');
    await expectNoReload(page);
    await card.getByRole('link', { name: `Host health for ${name}` }).click();
    await expect(page.getByRole('heading', { level: 1, name, exact: true })).toBeVisible();
    await expect(page.getByRole('table')).toContainText('Leave room for new builds.');
    await expect(page.getByRole('button', { name: /apply|tune/i })).toHaveCount(0);
    // Nothing to do and nothing held back: no panel on a healthy host's page.
    const next = page.getByRole('region', { name: 'Next step', exact: true });
    await expect(next).toHaveCount(0);
    await beat(false, true);
    await expect(page.getByText('Reboot pending', { exact: true })).toBeVisible();

    // A pending reboot gives the operator a next step: what is on the host, the
    // cordon that holds work back, and the one command to run on the machine.
    // The page never says drain, which stops a busy runner after five minutes,
    // and never offers to apply or tune anything itself.
    await expect(next).toContainText(`Nothing is running on ${name} right now`);
    await expect(next).toContainText('Taking new work');
    await expect(next).toContainText(`Run this on ${name}`);
    await expect(page.getByRole('main')).not.toContainText(/drain/i);
    await expect(next).not.toContainText('safe to reboot');
    await expect(page.getByRole('button', { name: /apply|tune/i })).toHaveCount(0);
    await next.getByRole('button', { name: 'Copy command' }).click();
    await expect
      .poll(() => page.evaluate(() => sessionStorage.getItem('copied')))
      .toBe('sudo zoomies doctor --interactive');

    // The reboot is a problem too, and its link opens the host, where the
    // report is, rather than the list of every host. (The other host problems
    // keep the list; a unit test covers those, because an offline host cannot
    // be made in a test's time.)
    //
    // The pill above moved at once, because a heartbeat publishes the host. The
    // problems list is not published by a heartbeat: it is worked out after each
    // reconcile pass, which is every ten seconds by default, so the row can be
    // that long coming and the seven-second default wait is shorter than the
    // interval. The wait here is longer than an interval plus a pass; the
    // interval is not shortened for the test.
    await page.getByRole('button', { name: /^Problems\./ }).click();
    const drawer = page.getByRole('dialog', { name: 'Problems' });
    const waiting = drawer
      .getByRole('listitem')
      .filter({ hasText: `host ${name} is waiting for a reboot` });
    await expect(waiting.getByRole('link', { name: 'Open the host' })).toHaveAttribute(
      'href',
      `/hosts/${credentials.host_id}`,
      { timeout: 20_000 },
    );
    await page.keyboard.press('Escape');
    await expect(drawer).toBeHidden();

    // A container's report is the container's view: most checks skipped, and
    // its image's distribution warned about for ever. The controller raises
    // nothing for it, so the page does not paint the host for it either.
    await heartbeat(
      page,
      credentials,
      [check('environment', 'Distribution', 'safe', 'warn')],
      false,
      true,
    );
    await expect(page.getByText('Partial report', { exact: true })).toBeVisible();
    // Its reboot flag is no verdict either, so the panel goes with it.
    await expect(next).toHaveCount(0);
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflow).toBe(false);
  } finally {
    await page.request.delete(`/api/v1/hosts/${credentials.host_id}`);
  }
});

// The monitor reports every tier, and `zoomies doctor` counts the safe one. A
// badge that counts the others reads "9 warnings" on a host whose every safe
// check passes, and disagrees with the command the page names.
test('the badge counts what zoomies doctor counts, and the page leads with what needs attention', async ({
  page,
}) => {
  const name = `health-order-${Date.now()}`;
  const credentials = await enrol(page, name);
  const disk = check('disk.space', 'Work directory free space', 'safe', 'error', {
    current: '3% free (11G)',
    recommended: '10% and 10 GiB free',
  });
  const watches = check('inotify.watches', 'File watches', 'safe', 'ok');
  const swappiness = check('memory.swappiness', 'Swappiness', 'aggressive', 'warn');
  const apport = check('service.apport', 'Dedicated host: apport.service', 'dedicated', 'warn');
  try {
    // Choices an operator has not made are not faults.
    await heartbeat(page, credentials, [watches, swappiness, apport]);
    await goto(page, '/hosts', 'Hosts');
    const card = page.getByRole('article', { name, exact: true });
    const pill = card.getByRole('link', { name: `Host health for ${name}` });
    await expect(pill).toContainText('Health OK');

    // A full disk and a pending reboot: the error is not hidden behind the reboot.
    await heartbeat(page, credentials, [watches, disk, swappiness, apport], true);
    await expect(pill).toContainText('1 health error · reboot pending');
    await pill.click();

    // The panel leads the page, ahead of the list of findings.
    await expect(page.getByRole('region', { name: 'Next step', exact: true })).toBeVisible();
    const attention = page.getByRole('region', { name: 'Needs attention' });
    await expect(attention.getByRole('listitem')).toHaveCount(1);
    await expect(attention).toContainText('Work directory free space');
    await expect(attention).toContainText('3% free (11G)');

    // The two choices are shown, and say what they are.
    await expect(page.getByText('Suggestion', { exact: true })).toHaveCount(2);
    // The passing check is folded away, behind the finding.
    await expect(page.getByText('1 passing or skipped check', { exact: true })).toBeVisible();
    await expect(page.getByText('inotify.watches')).toBeHidden();
    await page.getByText('1 passing or skipped check', { exact: true }).click();
    await expect(page.getByText('inotify.watches')).toBeVisible();

    // The link lands on the row, not just the page.
    await attention.getByRole('link', { name: 'Work directory free space' }).click();
    await expect(page).toHaveURL(/#disk\.space$/);
    await expect(page.locator('[id="disk.space"]')).toBeInViewport();
    await expect(page.getByRole('button', { name: /apply|tune/i })).toHaveCount(0);

    // The agent says a pending reboot twice -- the flag, and a kernel.pending
    // warning -- and the controller counts it once, as the reboot. The list
    // above is written a second time in TypeScript, and asking the real
    // controller how long it should be is what stops the two drifting.
    const reboot = check('kernel.pending', 'Pending reboot', 'safe', 'warn', {
      current: 'a newer kernel is installed',
    });
    const watchesWarn = check('inotify.watches', 'File watches', 'safe', 'warn', {
      current: '8192',
    });
    await heartbeat(page, credentials, [watchesWarn, disk, reboot, swappiness, apport], true);
    const counted = await summaryOf(page, credentials);
    expect(counted.errors, 'the full disk').toBe(1);
    expect(counted.warnings, 'the file watches, and not the reboot or the suggestions').toBe(1);
    await expect(attention.getByRole('listitem')).toHaveCount(counted.warnings + counted.errors);
    await expect(attention).not.toContainText('Pending reboot');
    await expect(page.getByText('1 health error · 1 warning · reboot pending')).toBeVisible();

    // A host whose only finding is a restart is waiting for one, not failing a
    // check: the pill says so and there is nothing left to fix under it.
    await heartbeat(page, credentials, [watches, reboot], true);
    expect((await summaryOf(page, credentials)).warnings).toBe(0);
    await expect(page.getByText('Reboot pending', { exact: true })).toBeVisible();
    await expect(attention).toHaveCount(0);
  } finally {
    await page.request.delete(`/api/v1/hosts/${credentials.host_id}`);
  }
});

// The page prints every row's reason, and a reason is text the agent wrote. The
// panel above them says cordon and never drain (a drain stops a runner that is
// still busy after five minutes), so a row that said drain would contradict it
// on the very host that has a reboot pending. These are the reasons the engine
// sends for the three rows that carry advice (internal/hosttune), so the page is
// shown the real thing rather than the empty reason every other fixture has. The
// source of that text is pinned by a Go test; this is the page's half.
test('the host page prints the reasons the agent sends, and none of them says drain', async ({
  page,
}) => {
  // No longer than the other health specs' names: the agent is the actor of its
  // own join in the audit log, and the tables spec fails a name wide enough to
  // push that table past a 360px screen.
  const name = `health-why-${Date.now()}`;
  const credentials = await enrol(page, name);
  const reboot =
    'reboot pending; cordon the host and wait until none of its runners is running a job, then reboot it manually';
  const mounts = 'advice only; review mount settings, with the host cordoned, before changing them';
  try {
    await heartbeat(
      page,
      credentials,
      [
        check('kernel.pending', 'Installed kernel awaiting reboot', 'safe', 'warn', {
          current: '6.9.0-1-generic',
          recommended: '6.10.0-2-generic',
          reason: reboot,
        }),
        check('work.filesystem', 'Work directory filesystem', 'safe', 'warn', {
          current: '/work: nfs4 rw',
          recommended: 'ext4 or XFS with noatime',
          reason: mounts,
        }),
        check('docker.filesystem', 'Docker root filesystem', 'safe', 'warn', {
          current: '/var/lib/docker: nfs4 rw',
          recommended: 'ext4 or XFS with noatime',
          reason: mounts,
        }),
      ],
      true,
    );
    await goto(page, `/hosts/${credentials.host_id}`, name);
    // The reasons are on the page, so the absence below means something.
    await expect(page.locator('[id="kernel.pending"]')).toContainText(reboot);
    await expect(page.locator('[id="work.filesystem"]')).toContainText(mounts);
    await expect(page.locator('[id="docker.filesystem"]')).toContainText(mounts);
    await expect(page.getByRole('region', { name: 'Next step', exact: true })).toBeVisible();
    await expect(page.getByRole('main')).not.toContainText(/drain/i);
  } finally {
    await page.request.delete(`/api/v1/hosts/${credentials.host_id}`);
  }
});

/** Whether the controller has this host cordoned, asked of the API and not of the page. */
async function cordonedOnServer(page: Page, credentials: Credentials): Promise<boolean> {
  const response = await page.request.get(`/api/v1/hosts/${credentials.host_id}`);
  expect(response.ok()).toBeTruthy();
  return ((await response.json()) as { cordoned?: boolean }).cordoned === true;
}

// The one thing the controller can do about a reboot is hold work back, and the
// host page is where somebody reading a reboot warning is. The page must say
// when the host is idle and cordoned (and not before the controller has agreed
// that it is), and must not leave a cordoned host with no way back once the
// reboot has cleared from its report -- a cordon nobody remembers is a host
// that takes no work for ever.
test('the host page cordons the host and says when it is idle and cordoned', async ({ page }) => {
  const name = `health-next-${Date.now()}`;
  const credentials = await enrol(page, name);
  const watches = check('inotify.watches', 'File watches', 'safe', 'ok');
  const next = page.getByRole('region', { name: 'Next step', exact: true });
  const toast = (title: string) => page.locator('.toast').filter({ hasText: title });
  try {
    await heartbeat(page, credentials, [watches], true);
    await goto(page, `/hosts/${credentials.host_id}`, name);
    await expect(next).toContainText(`Nothing is running on ${name} right now`);
    await expect(next).toContainText('Taking new work');
    await expect(next).toContainText('None');
    await plantMarker(page);

    // From the keyboard: the button keeps focus while the cordon lands and when
    // it turns into Uncordon, so Enter twice is cordon and uncordon.
    await next.getByRole('button', { name: 'Cordon this host', exact: true }).focus();
    await page.keyboard.press('Enter');
    const uncordon = next.getByRole('button', { name: 'Uncordon this host', exact: true });
    await expect(uncordon).toBeVisible();
    await expect(uncordon).toBeFocused();
    await expect(next.getByRole('status')).toContainText('Idle and cordoned: safe to reboot now');
    await expect(next).toContainText('Cordoned: no new runner is placed here');
    await expect(toast(`${name} cordoned`)).toBeVisible();
    await expectNoReload(page);
    expect(await cordonedOnServer(page, credentials)).toBe(true);
    const width = await documentWidth(page);
    expect(width.scrollWidth, 'the panel never scrolls the page sideways').toBeLessThanOrEqual(
      width.clientWidth,
    );

    // The card on the Hosts page says the same thing.
    await goto(page, '/hosts', 'Hosts');
    await expect(page.getByRole('article', { name, exact: true })).toContainText('Cordoned.');
    await goto(page, `/hosts/${credentials.host_id}`, name);
    await expect(next.getByRole('status')).toContainText('Idle and cordoned: safe to reboot now');

    // Back to taking work, and the verdict goes with it.
    await uncordon.click();
    await expect(next.getByRole('status')).toContainText(`Nothing is running on ${name} right now`);
    await expect(next.getByRole('button', { name: 'Cordon this host', exact: true })).toBeVisible();
    await expect(next).not.toContainText('safe to reboot');
    expect(await cordonedOnServer(page, credentials)).toBe(false);

    // Cordon it again, and let the reboot clear from the report, as it does
    // when the host comes back. The host is still cordoned, so its page still
    // has the way back -- and nothing to run, because nothing is waiting.
    await next.getByRole('button', { name: 'Cordon this host', exact: true }).click();
    await expect(uncordon).toBeVisible();
    await heartbeat(page, credentials, [watches], false);
    await expect(next.getByRole('status')).toContainText(
      `${name} is cordoned and nothing is waiting on it`,
    );
    await expect(next).toContainText('Uncordon it so that it takes work again.');
    await expect(next).not.toContainText('safe to reboot');
    await expect(next).not.toContainText(/drain/i);
    await expect(next.getByRole('button', { name: 'Copy command' })).toHaveCount(0);
    await expect(uncordon).toBeVisible();
    // With no command in the panel, the report keeps its own hint.
    await expect(page.getByText('Review changes locally with')).toBeVisible();

    // Uncordoning is the last thing left to do, so the panel has nothing more
    // to say and goes, and focus goes to the page's name rather than nowhere.
    await uncordon.click();
    await expect(next).toHaveCount(0);
    await expect(page.locator('#page-heading')).toBeFocused();
    expect(await cordonedOnServer(page, credentials)).toBe(false);
    await expect(page.getByRole('button', { name: /apply|tune/i })).toHaveCount(0);
  } finally {
    await page.request.delete(`/api/v1/hosts/${credentials.host_id}?force=true`);
  }
});

// A runner count is not a job count: a runner kept warm for a pool never
// finishes by itself. So a host with runners on it is never called safe, and the
// way to see which of them hold a job is one click away.
test('the host page does not call a host with runners on it safe to reboot', async ({ page }) => {
  const name = `health-busy-${Date.now()}`;
  const credentials = await enrol(page, name);
  try {
    await heartbeat(
      page,
      credentials,
      [check('inotify.watches', 'File watches', 'safe', 'ok')],
      true,
    );
    // A host with two runners on it is a rewritten list response: the page reads
    // its host from the list the app already holds, and a real runner would
    // need a job and a pool and a workflow behind it.
    await page.route(/\/api\/v1\/hosts(?:\?.*)?$/, async (route) => {
      const response = await route.fetch();
      const body = await response.json();
      body.items = body.items.map((host: { id: string }) =>
        host.id === credentials.host_id ? { ...host, active_runners: 2 } : host,
      );
      await route.fulfill({ json: body });
    });
    // And no stream, or the next heartbeat's host frame replaces the rewrite.
    await page.route('**/api/v1/events*', (route) =>
      route.fulfill({
        status: 200,
        headers: { 'content-type': 'text/event-stream', 'cache-control': 'no-store' },
        body: '',
      }),
    );
    await goto(page, `/hosts/${credentials.host_id}`, name);
    const next = page.getByRole('region', { name: 'Next step', exact: true });
    await expect(next).toContainText(`Runners are still on ${name}`);
    await expect(next).toContainText('2 runners');
    await expect(next).toContainText('never finishes by itself');
    await expect(next).not.toContainText('safe to reboot');
    await expect(
      next.getByRole('link', { name: 'Show the runners running a job' }),
    ).toHaveAttribute('href', `/runners?host_id=${credentials.host_id}&state=busy`);
    await expect(page.getByRole('main')).not.toContainText(/drain/i);
  } finally {
    await page.request.delete(`/api/v1/hosts/${credentials.host_id}?force=true`);
  }
});

// The Hosts page counts the hosts whose health pill is amber or red, and the tile,
// the chip and the cards are three views of one number. They are checked against
// each other, against the page's own live updates, and by pressing the chip and
// the tile, because a count that disagrees with the cards under it is the thing
// an operator stops believing first.
//
// One host is enrolled and walked through the states, rather than three held in
// each: joins are limited per address per minute, and this file shares the
// minute with the next. A warning, a reboot alone and a clean report are the
// three that matter, and one host can be each in turn. The expectations are
// `before + n`, never constants: the demo fleet's hosts have no OS report, and
// other specs on this shared controller leave hosts behind. The "Report stale"
// bucket is covered by the unit test only, because a past `checked_at` on the
// heartbeat path is not something this spec can promise the controller accepts.
test('the Hosts page counts the hosts that need attention, and the tile, the filter and the pills agree', async ({
  page,
}) => {
  const name = `health-count-${Date.now()}`;
  const watches = (status: Check['status']) => [
    check('inotify.watches', 'File watches', 'safe', status),
  ];
  const card = page.getByRole('article', { name, exact: true });
  let credentials: Credentials | undefined;
  try {
    // Enrolled and silent: the page does not know it until it has been heard
    // from, so what it counts first is the fleet without it.
    credentials = await enrol(page, name);
    await goto(page, '/hosts', 'Hosts');
    const tile = page.getByRole('link', { name: /^Need attention: \d+\./ });
    const before = Number(
      /^Need attention: (\d+)\./.exec((await tile.getAttribute('aria-label')) ?? '')?.[1],
    );
    expect(Number.isFinite(before), 'the tile says a number').toBe(true);
    await plantMarker(page);

    // A counted warning, live: the tile's number and the chip's agree, and both
    // are the one host the pill calls amber.
    await heartbeat(page, credentials, watches('warn'));
    const chips = page.getByRole('group', { name: 'Show hosts by OS health' });
    const chip = chips.getByRole('button', { name: /^Need attention \d+$/ });
    // A tile's number is its own element; nothing in the accessibility tree
    // separates it from the line under it, so it is reached through its tile.
    const value = page.locator('.metric', { has: tile }).locator('.value');
    await expect(value).toHaveText(String(before + 1));
    await expect(chip).toHaveAccessibleName(`Need attention ${before + 1}`);
    await expectNoReload(page);

    // The words: a heartbeat is Connected, and Healthy is the OS report's.
    await expect(page.getByText(/\d+ hosts? · \d+ connected · /)).toBeVisible();
    await expect(page.getByText('Hosts connected', { exact: true })).toBeVisible();
    await expect(page.getByText('Sending heartbeats')).toBeVisible();
    await expect(page.getByText('Healthy', { exact: true })).toHaveCount(0);

    // The chip filters, in the address, without a new history entry.
    const historyBefore = await page.evaluate(() => history.length);
    await chip.click();
    await expect(page).toHaveURL(/[?&]health=attention(&|$)/);
    await expect(chip).toHaveAttribute('aria-pressed', 'true');
    for (const other of [
      chips.getByRole('button', { name: /^All \d+$/ }),
      chips.getByRole('button', { name: /^Report stale \d+$/ }),
      chips.getByRole('button', { name: /^No report \d+$/ }),
    ]) {
      await expect(other).toHaveAttribute('aria-pressed', 'false');
    }
    expect(await page.evaluate(() => history.length), 'a chip replaces, it does not push').toBe(
      historyBefore,
    );
    await expect(page.getByRole('article')).toHaveCount(before + 1);
    await expect(card).toHaveCount(1);
    await expect(page.getByRole('status').filter({ hasText: 'Showing' })).toHaveText(
      new RegExp(
        `^Showing ${before + 1} of \\d+ hosts: OS settings below the recommendation, or a reboot pending\\.$`,
      ),
    );

    // A reboot on its own is counted too: it is the other thing the pill paints
    // amber. The pill moving is how the page shows the report has landed.
    await heartbeat(page, credentials, watches('ok'), true);
    await expect(card.getByRole('link', { name: `Host health for ${name}` })).toContainText(
      'Reboot pending',
    );
    await expect(value).toHaveText(String(before + 1));
    await expect(card).toHaveCount(1);

    // And a clean report is not: the host leaves the filtered view live, and
    // the tile and the chip follow it down. The list follows the report, not
    // the page's age.
    await heartbeat(page, credentials, watches('ok'));
    await expect(card).toHaveCount(0);
    await expect(page.getByRole('article')).toHaveCount(before);
    await expect(value).toHaveText(String(before));
    await expect(chip).toHaveAccessibleName(`Need attention ${before}`);
    await expectNoReload(page);

    // Unfiltered again, the clean host is among the cards: connected, and its
    // OS report fine. Two pills, two words.
    await goto(page, '/hosts', 'Hosts');
    await expect(card).toContainText('Connected');
    await expect(card).toContainText('Health OK');

    // The tile opens the same view, as a step of history that Back undoes.
    await tile.click();
    await expect(page).toHaveURL(/[?&]health=attention(&|$)/);
    await expect(chip).toHaveAttribute('aria-pressed', 'true');
    await expect(card).toHaveCount(0);
    await page.goBack();
    await expect(page).not.toHaveURL(/health=/);
    await expect(chips.getByRole('button', { name: /^All \d+$/ })).toHaveAttribute(
      'aria-pressed',
      'true',
    );
    await expect(card).toHaveCount(1);
  } finally {
    if (credentials) {
      await page.request.delete(`/api/v1/hosts/${credentials.host_id}?force=true`);
    }
  }
});
