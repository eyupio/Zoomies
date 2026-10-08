import { expect, test, type Page } from '@playwright/test';
import { browserOverride, documentWidth, goto } from './support/fixtures';
import { FakeAgent, check, enrolAgent, type Check } from './support/fake-agent';
test.use(browserOverride);

// The page the operator presses "Check now" on, against a host that really
// enrols, heartbeats, polls and answers over the agent API. The seeded embedded
// host is never used: it really answers, and a test must not depend on that.

const disk = (status: Check['status']) =>
  check('disk.space', 'Free space', status, { recommended: '10% free' });
const BUTTON = { name: 'Check now' } as const;

/** Enrol a host, give it a first report, and keep its agent beating and polling. */
async function withAgent(
  page: Page,
  prefix: string,
  body: (agent: FakeAgent, name: string) => Promise<void>,
  opts: { features?: string[]; first?: Check[] } = {},
): Promise<void> {
  // Short on purpose: agent names are capped, and the timestamp keeps them apart.
  const name = `${prefix}-${Date.now() % 1e8}`;
  const credentials = await enrolAgent(page, name);
  const agent = new FakeAgent(page, credentials, opts.features);
  try {
    await agent.beat(opts.first ?? [disk('warn')]);
    agent.start();
    await body(agent, name);
  } finally {
    await agent.stop();
    await page.request.delete(`/api/v1/hosts/${credentials.host_id}?force=true`);
  }
}

/** The line under the page name that says how the last ask went. */
const note = (page: Page) => page.locator('header .note');
/** What a screen reader is told: one polite region in the header, not the page body's. */
const said = (page: Page) => page.locator('.check-note output[aria-live="polite"]');

async function noApplyOrTune(page: Page): Promise<void> {
  await expect(page.getByRole('button', { name: /apply|tune/i })).toHaveCount(0);
}

async function checkedAtOf(page: Page, id: string): Promise<string> {
  const response = await page.request.get(`/api/v1/hosts/${id}`);
  const host = (await response.json()) as { doctor?: { checked_at: string } };
  return host.doctor?.checked_at ?? '';
}

// The whole point of the feature: ask, see the page say it asked, and see the
// answer arrive as the page changing -- fresh time, a change listed -- with no
// toast, because a background outcome is shown by the page.
test('Check now asks the agent and the page shows the changed report without a toast', async ({
  page,
}) => {
  await withAgent(page, 'chk-changed', async (agent, name) => {
    agent.setAnswer(() => ({ kind: 'report', results: [disk('ok')] }));
    const release = agent.hold();
    await goto(page, `/hosts/${agent.id}`, name);
    await expect(page.getByRole('region', { name: 'Needs attention' })).toBeVisible();
    await expect(note(page)).toHaveCount(0);
    const button = page.getByRole('button', BUTTON);
    await expect(button).toBeEnabled();
    await noApplyOrTune(page);

    await button.click();
    await expect(note(page)).toHaveText(`Asked ${name} to check itself…`);
    await expect(said(page)).toHaveText(`Asked ${name} to check itself…`);
    await expect(button).toHaveAttribute('aria-busy', 'true');
    expect(agent.checks.length).toBeLessThanOrEqual(1);

    release();
    await expect(note(page)).toHaveText(
      'Checked just now. 1 change since the last report, listed below.',
    );
    await expect(said(page)).toHaveText('Checked just now. The report has changed.');
    await expect(page.locator('.fresh')).toContainText('Report checked just now');
    await expect(
      page.getByRole('region', { name: 'Changed since you opened this page' }),
    ).toContainText('Free space, now OK');
    await expect(page.getByRole('region', { name: 'Needs attention' })).toHaveCount(0);
    // Success never toasts.
    await expect(page.locator('.toast')).toHaveCount(0);
    expect(agent.checks).toHaveLength(1);
    await noApplyOrTune(page);
  });
});

// An agent that looks and finds the same thing has still looked, and the page
// has to say so: otherwise a press that changes nothing looks like a press that
// did nothing.
test('an unchanged report still moves the checked time and says nothing has changed', async ({
  page,
}) => {
  await withAgent(page, 'chk-same', async (agent, name) => {
    agent.setAnswer(() => ({ kind: 'report', results: [disk('warn')] }));
    await goto(page, `/hosts/${agent.id}`, name);
    const before = await checkedAtOf(page, agent.id);
    await expect(page.getByRole('button', BUTTON)).toBeEnabled();
    await page.getByRole('button', BUTTON).click();
    await expect(note(page)).toHaveText(
      'Checked just now. Nothing has changed since the last report.',
    );
    await expect
      .poll(() => checkedAtOf(page, agent.id), { message: 'the stored report time moved' })
      .not.toBe(before);
    await expect(page.getByText('Changed since you opened this page')).toHaveCount(0);
  });
});

// A failure the person caused by pressing is told to them, and stays until read.
test('an agent that cannot run its checks is a sticky failure with the agent’s reason', async ({
  page,
}) => {
  await withAgent(page, 'chk-fail', async (agent, name) => {
    agent.setAnswer(() => ({ kind: 'failure', error: 'the disk check hung' }));
    await goto(page, `/hosts/${agent.id}`, name);
    await page.getByRole('button', BUTTON).click();
    await expect(note(page)).toHaveText(`${name} could not run its checks: the disk check hung`);
    const toast = page
      .locator('.toast[data-tone="error"]')
      .filter({ hasText: `Check now failed on ${name}` });
    await expect(toast).toBeVisible();
    // The inline text is not in a live region, so it is not read a second time.
    await expect(said(page)).toHaveText('');
    // The last report is still there.
    await expect(page.getByRole('region', { name: 'Needs attention' })).toBeVisible();
    await noApplyOrTune(page);
  });
});

// Its patience is not the controller's: a controller that restarted forgets the
// ask, and a page that waited for ever would be worse than one that gave up.
// Time is the page's own clock here, moved forward past the ceiling.
test('an agent that never answers is reported as not answering once the page has waited', async ({
  page,
}) => {
  await page.clock.install();
  await withAgent(page, 'chk-quiet', async (agent, name) => {
    agent.setAnswer(() => ({ kind: 'silence' }));
    await goto(page, `/hosts/${agent.id}`, name);
    await page.getByRole('button', BUTTON).click();
    await expect(note(page)).toHaveText(`Asked ${name} to check itself…`);
    await page.clock.fastForward(8_000);
    await expect(note(page)).toContainText(`Still waiting for ${name} to answer.`);
    await expect(said(page)).toHaveText(`Asked ${name} to check itself…`);
    await page.clock.fastForward(80_000);
    await expect(note(page)).toContainText(`${name} did not answer in time.`);
    await expect(note(page)).toContainText('the last report is still shown');
    await expect(
      page.locator('.toast[data-tone="error"]').filter({ hasText: `Check now failed on ${name}` }),
    ).toBeVisible();
    // The button is usable again rather than spinning for ever.
    await expect(page.getByRole('button', BUTTON)).not.toHaveAttribute('aria-busy', 'true');
  });
});

// The cooldown is the controller's, and the page does not spend a request to
// learn it. The button stays enabled so focus is not thrown away.
test('a second press inside the cooldown asks nothing and says how long to wait', async ({
  page,
}) => {
  await withAgent(page, 'chk-twice', async (agent, name) => {
    agent.setAnswer(() => ({ kind: 'report', results: [disk('warn')] }));
    const posts: string[] = [];
    page.on('request', (r) => {
      if (r.method() === 'POST' && r.url().endsWith('/health-check')) posts.push(r.url());
    });
    await goto(page, `/hosts/${agent.id}`, name);
    const button = page.getByRole('button', BUTTON);
    await button.click();
    await expect(note(page)).toContainText('Checked just now.');
    await button.click();
    await expect(note(page)).toHaveText(/^Checked just now\. You can check again in \d+ s\.$/);
    await expect(button).toBeEnabled();
    expect(posts).toHaveLength(1);
    expect(agent.checks).toHaveLength(1);
    // Said once, when the answer came; the countdown is not read out.
    await expect(said(page)).toHaveText(
      'Checked just now. Nothing has changed since the last report.',
    );
  });
});

// A container install, an old agent and a non-Linux host do not advertise the
// flag, and the page says why rather than offering a button that cannot work.
test('a host that does not advertise host-check has the button off, with the reason beside it', async ({
  page,
}) => {
  await withAgent(
    page,
    'chk-noflag',
    async (agent, name) => {
      let asked = 0;
      page.on('request', (r) => {
        if (r.method() === 'POST' && r.url().endsWith('/health-check')) asked++;
      });
      await goto(page, `/hosts/${agent.id}`, name);
      const button = page.getByRole('button', BUTTON);
      await expect(button).toBeDisabled();
      const reason = page.locator('#check-now-reason');
      await expect(reason).toBeVisible();
      await expect(reason).toContainText(`The agent on ${name} cannot check on request.`);
      await expect(reason).toContainText('container');
      await expect(button).toHaveAttribute('aria-describedby', 'check-now-reason');
      expect(asked).toBe(0);
      await noApplyOrTune(page);
    },
    { features: [] },
  );
});

async function signedInAs(page: Page, role: 'viewer' | 'operator'): Promise<void> {
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

// Asking spends a host's CPU, so a viewer is never offered it; they are told why.
test('a viewer sees no button and is told the operator role is needed', async ({ page }) => {
  await withAgent(page, 'chk-viewer', async (agent, name) => {
    // After enrolling, which a viewer could not do.
    await signedInAs(page, 'viewer');
    await goto(page, `/hosts/${agent.id}`, name);
    await expect(page.getByRole('region', { name: 'Needs attention' })).toBeVisible();
    await expect(page.getByRole('button', BUTTON)).toHaveCount(0);
    await expect(note(page)).toHaveText('Asking a host to check itself needs the operator role.');
  });
});

// A keyboard user asks and hears the answer without the button taking focus away.
test('the button is reachable and keeps focus from the ask to the answer', async ({ page }) => {
  await withAgent(page, 'chk-keys', async (agent, name) => {
    agent.setAnswer(() => ({ kind: 'report', results: [disk('ok')] }));
    await goto(page, `/hosts/${agent.id}`, name);
    const button = page.getByRole('button', BUTTON);
    await expect(button).toBeEnabled();
    await button.focus();
    await expect(button).toBeFocused();
    await page.keyboard.press('Enter');
    await expect(note(page)).toContainText('Checked just now.');
    await expect(button).toBeFocused();
    // Pressed again inside the cooldown, with the keyboard, focus still stays.
    await page.keyboard.press('Enter');
    await expect(note(page)).toContainText('You can check again in');
    await expect(button).toBeFocused();
  });
});

// Read-only monitoring on a phone is a requirement, and the lines are longer
// than the buttons beside them.
for (const width of [360, 412]) {
  test(`the button and its lines fit a ${width}px screen`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 });
    await withAgent(page, `chk-${width}`, async (agent, name) => {
      agent.setAnswer(() => ({ kind: 'report', results: [disk('ok')] }));
      const release = agent.hold();
      await goto(page, `/hosts/${agent.id}`, name);
      const button = page.getByRole('button', BUTTON);
      await button.click();
      await expect(note(page)).toContainText('Asked');
      let w = await documentWidth(page);
      expect(w.scrollWidth).toBeLessThanOrEqual(w.clientWidth);
      release();
      await expect(note(page)).toContainText('Checked just now.');
      await expect(button).toBeInViewport();
      w = await documentWidth(page);
      expect(w.scrollWidth).toBeLessThanOrEqual(w.clientWidth);
    });
  });

  test(`the reason a host cannot be asked fits a ${width}px screen`, async ({ page }) => {
    await page.setViewportSize({ width, height: 800 });
    await withAgent(
      page,
      `chk-r${width}`,
      async (agent, name) => {
        await goto(page, `/hosts/${agent.id}`, name);
        await expect(page.locator('#check-now-reason')).toBeVisible();
        const w = await documentWidth(page);
        expect(w.scrollWidth).toBeLessThanOrEqual(w.clientWidth);
        await expect(page.getByRole('button', BUTTON)).toBeInViewport();
      },
      { features: [] },
    );
  });
}
