import { expect, test, type Page } from '@playwright/test';
import { browserOverride, goto, plantMarker, expectNoReload } from './support/fixtures';
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
    await beat(false, true);
    await expect(page.getByText('Reboot pending', { exact: true })).toBeVisible();
    await expect(page.getByRole('main')).toContainText('Drain the host');

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

/* -- reading a host's report from a terminal -------------------------------------- */

/**
 * Enrol a host that has sent a report, and run the body on its page. The host is
 * removed afterwards whether or not the body passed: the suite shares one
 * controller.
 */
async function withReportingHost(
  page: Page,
  body: (host: { id: string; name: string }) => Promise<void>,
): Promise<void> {
  const name = `terminal-host-${Date.now()}`;
  const credentials = await enrol(page, name);
  try {
    await heartbeat(page, credentials, [check('disk.space', 'Free space', 'safe', 'ok')]);
    await body({ id: credentials.host_id, name });
  } finally {
    await page.request.delete(`/api/v1/hosts/${credentials.host_id}`);
  }
}

/**
 * The page is told who it is talking to. The fixture controller has
 * authentication off, so everybody there is an administrator and a token has no
 * account to belong to; the page asks the controller nothing different.
 */
async function signedInAs(
  page: Page,
  role: 'viewer' | 'operator' | 'admin',
  externalURL?: string,
): Promise<void> {
  await page.route('**/api/v1/meta', async (route) => {
    const response = await route.fetch();
    const body = (await response.json()) as Record<string, unknown>;
    return route.fulfill({
      response,
      json: {
        ...body,
        auth_disabled: false,
        bootstrap_required: false,
        ...(externalURL ? { external_url: externalURL } : {}),
      },
    });
  });
  await page.route('**/api/v1/auth/session', (route) =>
    route.fulfill({ json: { kind: 'token', id: 'tok_pretend', name: 'a token', role } }),
  );
}

/** What the page's token request is answered with: what the controller would send, for a token. */
function tokenAnswer(asked: Record<string, unknown>, made: string, lifeMs: number) {
  return {
    id: 'tok_pretend',
    name: asked.name,
    role: 'viewer',
    scopes: ['hosts:read'],
    prefix: 'zoo_pretend',
    revoked: false,
    created_at: new Date().toISOString(),
    expires_at: new Date(Date.now() + lifeMs).toISOString(),
    last_used_at: null,
    token: made,
  };
}

// `zoomies doctor --host` needs the controller's address and, unless the
// controller has authentication off, a token. The page hands over the command
// whole, because the machine it is pasted on is usually the host itself, which
// has neither to hand.
test('with authentication off, the host page gives a command with the address already in', async ({
  page,
}) => {
  await withReportingHost(page, async ({ id, name }) => {
    await goto(page, `/hosts/${id}`, name);
    const panel = page.getByRole('region', { name: 'Read this report from a terminal' });
    await expect(panel).toContainText('authentication off');
    await expect(panel.getByRole('button', { name: 'Make a command' })).toHaveCount(0);
    const origin = new URL(page.url()).origin;
    await expect(panel.locator('pre')).toHaveText(
      `zoomies doctor --host ${id} --verbose --url ${origin}`,
    );
    // The host itself needs none of it.
    await expect(panel).toContainText('zoomies doctor --verbose');
  });
});

test('with authentication on, the page makes a short-lived read-only token and puts it in the command', async ({
  page,
}) => {
  await withReportingHost(page, async ({ id, name }) => {
    const made = 'zoo_pretend_0123456789abcdefghij';
    const configured = 'https://zoomies.example.com';
    const asked: Array<Record<string, unknown>> = [];
    await signedInAs(page, 'viewer', configured);
    await page.route('**/api/v1/tokens', (route) => {
      if (route.request().method() !== 'POST') return route.fallback();
      asked.push(route.request().postDataJSON() as Record<string, unknown>);
      return route.fulfill({ status: 201, json: tokenAnswer(asked[0]!, made, 15 * 60_000) });
    });
    await page.addInitScript(() => {
      Object.defineProperty(navigator, 'clipboard', {
        configurable: true,
        value: { writeText: async (value: string) => sessionStorage.setItem('copied', value) },
      });
    });
    await page.clock.install();
    await goto(page, `/hosts/${id}`, name);
    const panel = page.getByRole('region', { name: 'Read this report from a terminal' });

    // Nothing is made until it is asked for, and there is no command to copy
    // that would fail.
    await expect(panel.locator('pre')).toHaveCount(0);
    await panel.getByRole('button', { name: 'Make a command' }).click();

    // What was asked for is what makes it safe to offer on a click: the least
    // role, reading hosts and nothing else, for a quarter of an hour, named for
    // the host by its id and not by the name the host gave itself.
    await expect.poll(() => asked.length).toBe(1);
    expect(asked[0]).toEqual({
      name: `Terminal: doctor ${id}`,
      role: 'viewer',
      scopes: ['hosts:read'],
      expires_in: '15m',
    });
    // The address is the one the controller was configured to hand out, and not
    // the one this browser happens to be using.
    const command = `zoomies doctor --host ${id} --verbose --url ${configured} --token ${made}`;
    await expect(panel.locator('pre')).toHaveText(command);
    await expect(panel).toContainText('can only read hosts');
    // What is copied is the command, whole.
    await panel.getByRole('button', { name: 'Copy the command' }).click();
    expect(await page.evaluate(() => sessionStorage.getItem('copied'))).toBe(command);
    // A long command does not push the page sideways, on a phone least of all.
    const overflow = await page.evaluate(
      () => document.documentElement.scrollWidth > window.innerWidth,
    );
    expect(overflow).toBe(false);

    // When the token ends the command goes with it, so a dead one is never what
    // gets copied, and a new one can be made.
    await page.clock.fastForward('16:00');
    await expect(panel.locator('pre')).toHaveCount(0);
    await expect(panel).toContainText('That token has ended');
    await expect(panel.getByRole('button', { name: 'Make another command' })).toBeVisible();
  });
});

// The page's clock ticks every ten seconds, and a command that outlived its token
// by up to that long is one somebody copies. So the command goes at the second the
// token ends: this one lasts two, on the real clock, with no tick near it.
test('the command goes at the second its token ends, not at the next tick of the page', async ({
  page,
}) => {
  await withReportingHost(page, async ({ id, name }) => {
    await signedInAs(page, 'viewer');
    await page.route('**/api/v1/tokens', (route) =>
      route.request().method() === 'POST'
        ? route.fulfill({
            status: 201,
            json: tokenAnswer(route.request().postDataJSON(), 'zoo_pretend_short_lived', 2_500),
          })
        : route.fallback(),
    );
    await goto(page, `/hosts/${id}`, name);
    const panel = page.getByRole('region', { name: 'Read this report from a terminal' });
    await panel.getByRole('button', { name: 'Make a command' }).click();
    await expect(panel.locator('pre')).toContainText('zoo_pretend_short_lived');
    await expect(panel.locator('pre')).toHaveCount(0, { timeout: 5_000 });
    await expect(panel).toContainText('That token has ended');
  });
});

test('a command that could not be made says why and leaves nothing to copy', async ({ page }) => {
  await withReportingHost(page, async ({ id, name }) => {
    await signedInAs(page, 'viewer');
    await page.route('**/api/v1/tokens', (route) =>
      route.request().method() === 'POST'
        ? route.fulfill({
            status: 422,
            json: { error: { code: 'invalid', message: 'tokens cannot be made right now' } },
          })
        : route.fallback(),
    );
    await goto(page, `/hosts/${id}`, name);
    const panel = page.getByRole('region', { name: 'Read this report from a terminal' });
    await panel.getByRole('button', { name: 'Make a command' }).click();
    await expect(
      page.locator('.toast[data-tone="error"]', { hasText: 'tokens cannot be made right now' }),
    ).toBeVisible();
    await expect(panel.locator('pre')).toHaveCount(0);
    await expect(panel.getByRole('button', { name: 'Make a command' })).toBeEnabled();
  });
});
