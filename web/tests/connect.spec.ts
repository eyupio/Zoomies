/**
 * Connecting a GitHub App, from the browser, against a GitHub the browser can
 * reach.
 *
 * The suite drives the real binary, but its fake GitHub is an in-process test
 * server -- so the connect and verify pages, which are the first two an
 * operator meets after creating an account, were exercised at every layer
 * except the one they live on. This project runs that same fake as a program on
 * a loopback port beside an empty controller, so the whole path is real: the
 * form, the seal, the probe, the verdict.
 *
 * It is also the suite's only fail-then-recover journey outside a wrong
 * password. Setting a fleet up is mostly a sequence of things that do not work
 * yet, and a page is only trustworthy if it says which one.
 */
import { createHmac, randomUUID } from 'node:crypto';
import { readFileSync } from 'node:fs';

import { expect, test } from '@playwright/test';
import { browserOverride, goto, reload } from './support/fixtures';

test.use(browserOverride);
test.describe.configure({ mode: 'serial' });

interface Fake {
  url: string;
  appId: string;
  installationId: string;
  privateKey: string;
}

/** What the fixture left behind when it started the fake. */
function fake(): Fake {
  return JSON.parse(readFileSync('test-results/fakegithub.json', 'utf8')) as Fake;
}

/** Fill the "existing App" form and submit it. */
async function connectWith(page: import('@playwright/test').Page, key: string): Promise<void> {
  const details = fake();
  await page.getByRole('button', { name: 'Connect GitHub' }).first().click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  await dialog.getByRole('tab', { name: 'Existing App' }).click();

  await dialog.getByLabel('Organisation login').fill('acme');
  await dialog.getByLabel('App ID').fill(details.appId);
  await dialog.getByLabel('Installation ID').fill(details.installationId);
  await dialog.getByLabel('API base URL').fill(details.url);
  await dialog.getByLabel('Private key').fill(key);
  await dialog.getByRole('button', { name: 'Connect', exact: true }).click();
}

test('an App connected with an unusable key says so, and works once it is fixed', async ({
  page,
}) => {
  await goto(page, '/installations', 'Installations');

  // A key that is not a key: the commonest paste error there is, because the
  // .pem file GitHub hands over is downloaded once and easily confused with
  // the App's public identifiers.
  await connectWith(
    page,
    '-----BEGIN RSA PRIVATE KEY-----\nnot actually a key\n-----END RSA PRIVATE KEY-----\n',
  );

  const dialog = page.getByRole('dialog');
  // The dialog does not end at a recorded row: it ends when the operator knows
  // whether the credentials work.
  await expect(dialog).toContainText(/something is missing|did not work|private key/i, {
    timeout: 15_000,
  });
  // The footer's Close, not the dialog's corner icon: both are named Close.
  await dialog.getByRole('button', { name: 'Close', exact: true }).last().click();

  // And the page agrees: an installation that cannot authenticate is not shown
  // as healthy just because the row exists.
  const card = page.getByRole('article').filter({ hasText: 'acme' }).first();
  await expect(card).toBeVisible();
  await card.getByRole('button', { name: /Verify/ }).click();
  const verify = page.getByRole('dialog');
  await expect(verify).toContainText(/private key|could not|not a PEM/i, { timeout: 15_000 });
  await page.keyboard.press('Escape');

  // Now the real key, without taking the fleet apart to do it. Until this,
  // replacing a key meant disconnecting the installation -- which takes its
  // pools and their runner rows with it -- because the route existed and
  // nothing surfaced it.
  await card.getByRole('button', { name: 'Replace key' }).click();
  const replace = page.getByRole('dialog');
  await expect(replace).toBeVisible();

  // A refusal must not lock the form: it is about the text that was sent, so
  // pasting something else withdraws it and the button comes back. The server
  // is answered for here because a public key passes the browser's own
  // check, and the real one would accept the fixture's key on the next line.
  await page.route('**/api/v1/installations/*', async (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback();
    await route.fulfill({
      status: 422,
      contentType: 'application/json',
      body: JSON.stringify({
        error: { code: 'validation_failed', message: 'the private key was refused' },
        errors: [{ field: 'private_key', message: 'That is a public key, not a private one.' }],
      }),
    });
  });
  const replaceButton = replace.getByRole('button', { name: 'Replace the key' });
  await replace.getByLabel('Private key').fill('-----BEGIN PUBLIC KEY-----\nabc\n');
  await replaceButton.click();
  await expect(replace).toContainText('That is a public key, not a private one.');
  await expect(replaceButton).toBeDisabled();
  await replace.getByLabel('Private key').fill(fake().privateKey);
  await expect(replace).not.toContainText('That is a public key, not a private one.');
  await expect(replaceButton).toBeEnabled();
  await page.unroute('**/api/v1/installations/*');

  await replaceButton.click();

  // Replacing it verifies straight away: "it is stored" is not the answer
  // somebody replacing a broken credential came for.
  const ok = page.getByRole('dialog');
  await expect(ok).toContainText(/credentials work/i, { timeout: 15_000 });
  await expect(ok).toContainText(/Repositories this installation can see/i);
  await expect(ok).toContainText('acme/widgets');
});

/** The secret the tests below give the installation, so a delivery can be signed. */
const WEBHOOK_SECRET = 'checklist-regression-secret';

interface JobEvent {
  action: 'queued' | 'in_progress' | 'completed';
  id: number;
  labels: string[];
  createdAt: Date;
  startedAt?: Date;
  completedAt?: Date;
  runnerName?: string;
  conclusion?: 'success' | 'failure';
}

/**
 * GitHub's `workflow_job` delivery, signed the way GitHub signs it.
 *
 * The controller cannot tell this from the real thing, which is the point: the
 * checklist's behaviour on a first job is a property of the stream a real
 * organisation produces, and a runner that actually ran a container would add
 * a Docker dependency to a test about a panel.
 */
async function deliverJob(
  request: import('@playwright/test').APIRequestContext,
  job: JobEvent,
): Promise<void> {
  const body = JSON.stringify({
    action: job.action,
    workflow_job: {
      id: job.id,
      run_id: job.id - 100_000,
      run_attempt: 1,
      workflow_name: 'Zoomies test job',
      name: 'hello',
      labels: job.labels,
      status: job.action,
      conclusion: job.conclusion ?? null,
      created_at: job.createdAt.toISOString(),
      started_at: job.startedAt?.toISOString() ?? null,
      completed_at: job.completedAt?.toISOString() ?? null,
      html_url: `https://github.com/acme/widgets/actions/runs/${job.id - 100_000}/job/${job.id}`,
      head_branch: 'main',
      head_sha: 'a'.repeat(40),
      runner_id: job.runnerName ? 7 : 0,
      runner_name: job.runnerName ?? '',
      steps: [],
    },
    repository: { full_name: 'acme/widgets' },
    installation: { id: Number(fake().installationId) },
  });
  const signature = createHmac('sha256', WEBHOOK_SECRET).update(body).digest('hex');
  const delivery = await request.post('/webhooks/github', {
    headers: {
      'content-type': 'application/json',
      'x-github-event': 'workflow_job',
      'x-github-delivery': randomUUID(),
      'x-hub-signature-256': `sha256=${signature}`,
    },
    data: body,
  });
  expect(delivery.status()).toBe(202);
}

test('a job on somebody else\u2019s runner does not retire the setup checklist', async ({
  page,
  request,
}) => {
  // The installation the test above connected, and nothing else: no host, no
  // pool, and no job of this fleet's. The checklist is how an operator in
  // exactly this state finds out what is left.
  await goto(page, '/', 'Overview');
  const checklist = page.getByRole('heading', { name: 'Finish setting up' });
  await expect(checklist).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem('zoomies.firstrun.dismissed'))).toBeNull();

  // A secret the test knows, so a delivery can be signed the way GitHub signs.
  const installations = (await (await request.get('/api/v1/installations')).json()) as {
    items: { id: string }[];
  };
  const patched = await request.patch(`/api/v1/installations/${installations.items[0]!.id}`, {
    data: { webhook_secret: WEBHOOK_SECRET },
  });
  expect(patched.ok()).toBe(true);

  // GitHub reports every job in an installed repository, including the ones
  // that run on its own runners. This one asks for ubuntu-latest, so nothing
  // here will ever pick it up. The checklist used to count it as "a job has
  // run", retire itself and remember that for good -- before a pool existed.
  await deliverJob(request, {
    action: 'queued',
    id: 900_001,
    labels: ['ubuntu-latest'],
    createdAt: new Date(),
  });

  // The job is counted, but as everybody's and not as ours: the unscoped total
  // moved and the fleet's own did not. That difference is the whole bug.
  await expect
    .poll(async () => ((await (await request.get('/api/v1/stats')).json()) as Stats).queued_jobs)
    .toBe(1);
  const stats = (await (await request.get('/api/v1/stats')).json()) as Stats;
  expect(stats.fleet?.queued_jobs ?? 0).toBe(0);

  // A fresh load reads the new numbers straight away, and the checklist that
  // is about this fleet has not noticed anyone else's job.
  await reload(page, 'Overview');
  await expect(checklist).toBeVisible();
  expect(await page.evaluate(() => localStorage.getItem('zoomies.firstrun.dismissed'))).toBeNull();
});

test('the first job on the fleet is announced where the checklist was, with its numbers', async ({
  page,
  request,
}) => {
  // A pool for the job to ask for. This fixture has no host, so no runner will
  // ever start for it; what is under test is the page, and what it does when
  // GitHub says a runner of this fleet has taken a job.
  const installations = (await (await request.get('/api/v1/installations')).json()) as {
    items: { id: string }[];
  };
  const created = await request.post('/api/v1/pools', {
    data: {
      name: 'moment',
      installation_id: installations.items[0]!.id,
      labels: ['zoomies-moment'],
      backend: 'docker',
    },
  });
  expect(created.status(), await created.text()).toBe(201);

  await goto(page, '/', 'Overview');
  const checklist = page.getByRole('heading', { name: 'Finish setting up' });
  await expect(checklist).toBeVisible();

  // With a pool, the last step stops asking an operator to edit a real
  // repository and hands over a file that touches nothing of theirs. It is
  // whole -- a trigger as well as a job -- and asks for this pool and no other.
  const workflow = page.getByRole('group', { name: 'The test workflow' });
  await expect(workflow).toContainText('on: workflow_dispatch');
  await expect(workflow).toContainText('runs-on: zoomies-moment');

  // GitHub reports the job queued, then started on a runner, then finished.
  const queued = new Date(Date.now() - 20_000);
  const started = new Date(queued.getTime() + 4_000);
  const finished = new Date(started.getTime() + 5_000);
  const job = { id: 900_002, labels: ['zoomies-moment'], createdAt: queued };
  await deliverJob(request, { ...job, action: 'queued' });
  await deliverJob(request, {
    ...job,
    action: 'in_progress',
    startedAt: started,
    runnerName: 'zoomies-moment-x1',
  });

  // The checklist does not just vanish: it says the thing it was waiting for
  // has happened, and where.
  const running = page.getByRole('heading', {
    name: 'Your first job is running on zoomies-moment-x1',
  });
  await expect(running).toBeVisible({ timeout: 20_000 });
  await expect(checklist).toHaveCount(0);

  await deliverJob(request, {
    ...job,
    action: 'completed',
    startedAt: started,
    completedAt: finished,
    runnerName: 'zoomies-moment-x1',
    conclusion: 'success',
  });
  await expect(
    page.getByRole('heading', { name: 'Your first job ran on zoomies-moment-x1' }),
  ).toBeVisible({ timeout: 20_000 });
  // The two numbers the product is judged on, in words.
  await expect(page.getByRole('status').filter({ hasText: 'It waited' })).toContainText(
    'It waited 4.0s for a runner and ran for 5.0s.',
  );
  await expect(page.getByRole('link', { name: 'See it on the Jobs page' })).toHaveAttribute(
    'href',
    /^\/jobs\?q=hello&repo=acme%2Fwidgets$/,
  );

  // It is a moment, not furniture: the next visit finds the Overview as it
  // will be from now on, and the checklist does not come back.
  await reload(page, 'Overview');
  await expect(page.getByRole('heading', { name: /^Your first job/ })).toHaveCount(0);
  await expect(checklist).toHaveCount(0);
});

interface Stats {
  queued_jobs: number;
  fleet?: { queued_jobs?: number };
}
