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
import { readFileSync } from 'node:fs';

import { expect, test, type Locator, type Page } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

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

/*
 * The refusal that used to be a dead end.
 *
 * This fixture has no external URL, which is what an install with no public
 * address looks like. The dialog refuses to build an App then, because the
 * webhook URL is fixed when GitHub creates it -- and it used to say only "set a
 * configuration key, restart, and come back": no field, no restart command, and
 * no mention of the way through for a home lab, which is the other tab. These
 * pin the two ways out it offers now.
 *
 * They run before the journey below, which connects an installation: the page
 * and its dialog are in their first-run state only until it has.
 */
const ADDRESS = 'https://zoomies.example.test';

async function openConnect(page: Page): Promise<Locator> {
  await goto(page, '/installations', 'Installations');
  await page.getByRole('button', { name: 'Connect GitHub' }).first().click();
  const dialog = page.getByRole('dialog', { name: 'Connect GitHub' });
  await expect(dialog).toBeVisible();
  // A dialog takes focus a frame after it opens. A test that fills a field and
  // presses a button inside that frame -- which a loaded machine makes easy --
  // has its own focus taken back, and then asserts on a cursor that is not where
  // it put it. A person cannot act that fast, so this waits for what they never see.
  await expect(dialog.locator(':focus')).toHaveCount(1);
  return dialog;
}

/**
 * Answer the save the way a controller that accepts it would.
 *
 * This fixture runs with authentication off, and the controller refuses an
 * external URL on an instance anybody can reach without signing in -- the
 * refusal test below is exactly that -- so the successful save is staged here.
 * That the real controller accepts the same request, and holds it for the next
 * restart, is pinned where authentication is on, in first-run.spec.ts.
 */
async function acceptTheAddress(page: Page): Promise<void> {
  await page.route('**/api/v1/settings', async (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback();
    await route.fulfill({ json: {} });
  });
}

test('the refusal offers the address and the polling route, not only a configuration key', async ({
  page,
}) => {
  const dialog = await openConnect(page);

  await expect(dialog.getByText('Zoomies has no external URL yet')).toBeVisible();
  await expect(
    dialog.getByRole('heading', { name: 'Give Zoomies the address GitHub will use' }),
  ).toBeVisible();
  await expect(
    dialog.getByRole('heading', { name: 'No public address, for example a home lab?' }),
  ).toBeVisible();
  // The one sentence a home-lab operator needs: what they give up, and what it costs.
  await expect(dialog).toContainText(
    'Zoomies will poll GitHub for queued jobs instead of receiving webhooks, which reacts in tens of seconds rather than instantly.',
  );
  await expect(dialog.getByRole('tab')).toHaveText(['New App', 'Existing App']);
  // Still refused: nothing about the way out changes what is being protected.
  await expect(dialog.getByRole('button', { name: 'Continue to GitHub' })).toBeDisabled();
  // This browser is open on 127.0.0.1, which is exactly what must not be offered.
  await expect(dialog.getByLabel('Public address')).toHaveValue('');
});

test('both tab labels are whole on a phone', async ({ page }) => {
  // "Use an App you already have" was clipped to "Use an App you already h" in
  // a tab list that scrolls sideways, so the way through was the part of the
  // dialog that could not be read.
  await page.setViewportSize({ width: 375, height: 812 });
  const dialog = await openConnect(page);
  const list = dialog.getByRole('tablist');
  const { scrollWidth, clientWidth } = await list.evaluate((el) => ({
    scrollWidth: el.scrollWidth,
    clientWidth: el.clientWidth,
  }));
  expect(scrollWidth, 'the tab list scrolls sideways, so a label is cut off').toBeLessThanOrEqual(
    clientWidth,
  );
  // And each tab ends inside the list, which is what "whole" means to a reader.
  const edge = (await list.boundingBox())!;
  for (const name of ['New App', 'Existing App']) {
    const box = (await dialog.getByRole('tab', { name }).boundingBox())!;
    expect(box.x + box.width, `the ${name} tab runs past the end of the list`).toBeLessThanOrEqual(
      edge.x + edge.width + 0.5,
    );
  }
});

test('an address only this machine can reach is refused before anything is sent', async ({
  page,
}) => {
  const sent: string[] = [];
  page.on('request', (request) => {
    if (request.method() === 'PATCH' && request.url().endsWith('/api/v1/settings')) {
      sent.push(request.url());
    }
  });
  const dialog = await openConnect(page);
  const box = dialog.getByLabel('Public address');

  await box.fill('http://localhost:8080');
  // Leaving the box says nothing. The commonest way of leaving it is pressing
  // Save, and an error that appears when the pointer goes down moves the button
  // out from under it: the click is lost, on a button that visibly did nothing.
  await box.blur();
  await expect(dialog.getByRole('alert')).toHaveCount(0);

  // Pressed the way a person presses it, with a beat between down and up. A
  // click that is down and up in the same instant cannot be moved from under.
  await dialog.getByRole('button', { name: 'Save address' }).click({ delay: 150 });
  await expect(
    dialog.getByRole('alert').filter({ hasText: 'only works on this machine' }),
  ).toBeVisible();
  await expect(dialog.getByText('Saved', { exact: true })).toHaveCount(0);
  // The first invalid field takes focus on submit, as it does on every form here.
  await expect(box).toBeFocused();

  // From then on it follows what is typed, and a typo is told what to write
  // rather than that it is invalid.
  await box.fill('zoomies.example.test');
  await expect(
    dialog.getByRole('alert').filter({ hasText: 'Write it as a full address' }),
  ).toBeVisible();
  expect(sent, 'nothing was sent for an address that was refused').toEqual([]);
});

test('saving the address waits for the restart, then unlocks the form by itself', async ({
  page,
}) => {
  // The restart is staged too, because the fixture's controller is not one the
  // test may stop. The setting is restart-scoped, so a real save leaves /meta
  // answering as it did -- which is the point of the wait.
  await acceptTheAddress(page);
  let controller: 'before' | 'away' | 'after' = 'before';
  await page.route('**/api/v1/meta', async (route) => {
    if (controller === 'away') return route.abort('connectionrefused');
    const response = await route.fetch();
    const meta = (await response.json()) as Record<string, unknown>;
    if (controller === 'after') {
      meta.external_url = ADDRESS;
      meta.webhook_url = `${ADDRESS}/webhooks/github`;
    }
    await route.fulfill({ response, json: meta });
  });

  const dialog = await openConnect(page);
  // Typed before the address is saved, to show the wait does not cost it.
  await dialog.getByLabel('Organisation login').fill('acme');

  const saving = page.waitForRequest(
    (request) => request.method() === 'PATCH' && request.url().endsWith('/api/v1/settings'),
  );
  // A trailing slash is what people paste; the controller strips it, so it is
  // saved without one and the two can be recognised as the same address.
  await dialog.getByLabel('Public address').fill(`${ADDRESS}/`);
  await dialog.getByRole('button', { name: 'Save address' }).click();
  expect((await saving).postDataJSON()).toEqual({ 'server.external_url': ADDRESS });

  // Said plainly, with the way to restart whichever way it was installed.
  await expect(dialog.getByText('Saved', { exact: true })).toBeVisible();
  // The button that was pressed went with the box it sat under: the cursor goes
  // to the result rather than to nothing.
  await expect(dialog.getByRole('group', { name: 'Address saved' })).toBeFocused();
  await expect(dialog).toContainText(`Zoomies will use ${ADDRESS} once the controller restarts.`);
  await expect(dialog).toContainText('whichever of these matches how you installed it');
  for (const command of [
    'sudo systemctl restart zoomies',
    'zoomies deployment restart',
    'docker compose restart zoomies',
  ]) {
    await expect(dialog.getByText(command, { exact: true })).toBeVisible();
    await expect(
      dialog.getByRole('button', { name: `Copy the command: ${command}` }),
    ).toBeVisible();
  }
  await expect(dialog.getByText(/Waiting for the restart/)).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Continue to GitHub' })).toBeDisabled();

  // The controller goes away, which is what a restart looks like from here.
  controller = 'away';
  await expect(dialog.getByText('Waiting for the controller to come back')).toBeVisible({
    timeout: 10_000,
  });
  await expect(dialog.getByRole('button', { name: 'Continue to GitHub' })).toBeDisabled();

  // And comes back with the address: the form unlocks without a click, and
  // what was typed is where it was left.
  controller = 'after';
  await expect(dialog.getByText(/Zoomies builds a GitHub App manifest/)).toBeVisible({
    timeout: 10_000,
  });
  await expect(dialog.getByText(`${ADDRESS}/webhooks/github`)).toBeVisible();
  await expect(dialog.getByLabel('Organisation login')).toHaveValue('acme');
  await expect(dialog.getByRole('button', { name: 'Continue to GitHub' })).toBeEnabled();
  // What had the cursor went with the card, and it is back on the form.
  await expect(dialog.getByRole('group', { name: 'Describe the App' })).toBeFocused();
});

test('the wait does not take the cursor from somebody who has moved on to typing', async ({
  page,
}) => {
  await acceptTheAddress(page);
  let controller: 'before' | 'after' = 'before';
  await page.route('**/api/v1/meta', async (route) => {
    const response = await route.fetch();
    const meta = (await response.json()) as Record<string, unknown>;
    if (controller === 'after') {
      meta.external_url = ADDRESS;
      meta.webhook_url = `${ADDRESS}/webhooks/github`;
    }
    await route.fulfill({ response, json: meta });
  });
  const dialog = await openConnect(page);
  await dialog.getByLabel('Public address').fill(ADDRESS);
  await dialog.getByRole('button', { name: 'Save address' }).click();
  await expect(dialog.getByText('Saved', { exact: true })).toBeVisible();

  // Typing the organisation while the restart happens is what the dialog invites.
  const organisation = dialog.getByLabel('Organisation login');
  await organisation.click();
  controller = 'after';
  await expect(dialog.getByText(/Zoomies builds a GitHub App manifest/)).toBeVisible({
    timeout: 10_000,
  });
  await expect(organisation).toBeFocused();
});

test('what was typed before the address was saved survives a reload during the restart', async ({
  page,
}) => {
  await acceptTheAddress(page);
  const dialog = await openConnect(page);
  await dialog.getByLabel('Organisation login').fill('acme');
  await dialog.getByLabel('Public address').fill(ADDRESS);
  await dialog.getByRole('button', { name: 'Save address' }).click();
  await expect(dialog.getByText('Saved', { exact: true })).toBeVisible();

  // Progress is otherwise written only once a manifest exists, so without this
  // a reload while the controller restarts would have cost the organisation.
  await page.reload({ waitUntil: 'domcontentloaded' });
  await page.getByRole('button', { name: 'Connect GitHub' }).first().click();
  await expect(dialog.getByLabel('Organisation login')).toHaveValue('acme');
});

test('Enter in the address box saves the address', async ({ page }) => {
  // The box sits inside the App's form, whose own submit is locked while there
  // is no address, so without a handler of its own Enter did nothing at all.
  await acceptTheAddress(page);
  const dialog = await openConnect(page);
  const box = dialog.getByLabel('Public address');
  await box.fill(ADDRESS);
  await box.press('Enter');
  await expect(dialog.getByText('Saved', { exact: true })).toBeVisible();
});

test('a different address can be given before the restart, with the box where it was', async ({
  page,
}) => {
  await acceptTheAddress(page);
  const dialog = await openConnect(page);
  await dialog.getByLabel('Public address').fill('https://zoomies.example.tset');
  await dialog.getByRole('button', { name: 'Save address' }).click();
  await expect(dialog.getByText('Saved', { exact: true })).toBeVisible();

  // A typo seen before the restart costs a click, not a restart.
  await dialog.getByRole('button', { name: 'Use a different address' }).click();
  const box = dialog.getByLabel('Public address');
  await expect(box).toBeFocused();
  await expect(box).toHaveValue('https://zoomies.example.tset');
  await expect(dialog.getByText('Saved', { exact: true })).toHaveCount(0);

  const saving = page.waitForRequest(
    (request) => request.method() === 'PATCH' && request.url().endsWith('/api/v1/settings'),
  );
  await box.fill(ADDRESS);
  await dialog.getByRole('button', { name: 'Save address' }).click();
  expect((await saving).postDataJSON()).toEqual({ 'server.external_url': ADDRESS });
  await expect(dialog).toContainText(`Zoomies will use ${ADDRESS} once the controller restarts.`);
});

test('a save that lands after the dialog was closed is not waiting in it next time', async ({
  page,
}) => {
  let release = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route('**/api/v1/settings', async (route) => {
    if (route.request().method() !== 'PATCH') return route.fallback();
    await held;
    await route.fulfill({ json: {} });
  });
  const dialog = await openConnect(page);
  await dialog.getByLabel('Public address').fill(ADDRESS);
  await dialog.getByRole('button', { name: 'Save address' }).click();

  // Closed with the request still out.
  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  const answered = page.waitForResponse(
    (response) =>
      response.request().method() === 'PATCH' && response.url().endsWith('/api/v1/settings'),
  );
  release();
  await answered;
  // Long enough for the page to have acted on the answer, which is the thing
  // being asserted not to have happened.
  await page.waitForTimeout(300);

  await page.getByRole('button', { name: 'Connect GitHub' }).first().click();
  await expect(dialog).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Save address' })).toBeVisible();
  await expect(dialog.getByText('Saved', { exact: true })).toHaveCount(0);
});

test('closing the dialog stops it asking the controller', async ({ page }) => {
  await acceptTheAddress(page);
  let asked = 0;
  await page.route('**/api/v1/meta', async (route) => {
    asked += 1;
    await route.fallback();
  });
  const dialog = await openConnect(page);
  await dialog.getByLabel('Public address').fill(ADDRESS);
  await dialog.getByRole('button', { name: 'Save address' }).click();
  await expect(dialog.getByText('Saved', { exact: true })).toBeVisible();

  // It is asking: the wait is a poll, not a one-off.
  const whenSaved = asked;
  await expect.poll(() => asked, { timeout: 10_000 }).toBeGreaterThan(whenSaved);

  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  const whenClosed = asked;
  // Two intervals and a little over. A timer left running would have asked twice.
  await page.waitForTimeout(4_500);
  expect(asked, 'the dialog kept asking after it was closed').toBe(whenClosed);
});

test('a refusal from the controller is shown in its own words, and nothing is claimed saved', async ({
  page,
}) => {
  // A real refusal, not a staged one. This fixture has authentication off, and
  // an external URL on such an instance would leave a controller that will not
  // start, so the settings API says no -- with a reason that names no field of
  // its own, because it is the combination that is wrong. The dialog says what
  // the API said, where every other refusal in it goes.
  const dialog = await openConnect(page);
  await dialog.getByLabel('Public address').fill(ADDRESS);
  await dialog.getByRole('button', { name: 'Save address' }).click();

  const refusal = dialog.getByRole('alert');
  await expect(refusal).toContainText('that would leave a controller that will not start');
  await expect(refusal).toContainText('security.disable_auth');
  await expect(dialog.getByText('Saved', { exact: true })).toHaveCount(0);
  // Not locked by it: the box is still there to try again, or to be given up on.
  await expect(dialog.getByRole('button', { name: 'Save address' })).toBeEnabled();
  await expect(dialog.getByLabel('Public address')).toHaveValue(ADDRESS);
});

test('the polling route opens the existing-App tab, which the missing address does not lock', async ({
  page,
}) => {
  const dialog = await openConnect(page);
  await dialog.getByRole('button', { name: 'Connect an existing App' }).click();

  const tab = dialog.getByRole('tab', { name: 'Existing App' });
  await expect(tab).toHaveAttribute('aria-selected', 'true');
  // The button that was pressed went with its panel; focus lands on the tab, not nowhere.
  await expect(tab).toBeFocused();

  // This tab's submit waits for the form and for nothing else: there is still
  // no address, and it is the path that does not need one.
  const connect = dialog.getByRole('button', { name: 'Connect', exact: true });
  await expect(connect).toBeDisabled();
  await dialog.getByLabel('Organisation login').fill('acme');
  await dialog.getByLabel('App ID').fill('1234');
  await dialog.getByLabel('Installation ID').fill('5678');
  await dialog.getByLabel('Private key').fill('-----BEGIN RSA PRIVATE KEY-----\nabc\n');
  await expect(connect).toBeEnabled();
});

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
