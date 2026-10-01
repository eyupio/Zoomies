/**
 * The screens an operator meets before there is anything to monitor.
 *
 * These run against their own server -- authentication on, an empty database --
 * because the main harness disables auth and seeds a fleet, which makes both of
 * these screens unreachable. That gap is why a dead focus jump, a swallowed
 * caps-lock warning and an Overview whose only button led into a refusal all
 * survived in an otherwise well-tested UI.
 *
 * The database is per-run and the bootstrap route closes the moment an account
 * exists, so the order here is load-bearing: the bootstrap tests come first and
 * the last of them creates the account the rest sign in with.
 */
import { test, expect } from '@playwright/test';
// The bootstrap route asks for the token the controller printed at startup, so
// a test creating the first account fetches it the way an operator reads
// `docker compose logs`. The fixture captures the line into a file.
import { browserOverride, setupToken } from './support/fixtures';

const ADMIN = { username: 'ada', password: 'correct horse battery staple' };

test.use(browserOverride);
test.describe.configure({ mode: 'serial' });

/** Each test gets its own context, so anything after the bootstrap signs in. */
async function signIn(page: import('@playwright/test').Page): Promise<void> {
  await page.goto('/login');
  await page.fill('input[name="username"]', ADMIN.username);
  await page.fill('input[name="password"]', ADMIN.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();
}

test('the first screen says what it is, where it sits, and what follows', async ({ page }) => {
  await page.goto('/');

  const heading = page.getByRole('heading', { level: 1 });
  await expect(heading).toHaveText('Create the first account');
  // An operator arriving from `docker compose up` has no way to know whether
  // this account finishes setup or begins it. It says which end of the
  // sequence this is and not how long the sequence is: that depends on whether
  // the controller has an agent of its own, which this page runs too early to
  // ask -- and the page used to claim four while the Overview's checklist drew
  // five on exactly the install that reaches it.
  await expect(page.getByText('First step')).toBeVisible();
  await expect(page.getByText(/of 4|of four/i)).toHaveCount(0);
  await expect(page.getByText(/connect a GitHub App|connect GitHub/i)).toBeVisible();
  // The form asks for the token before anything else, and says where to find
  // it: an operator who has not read the log cannot finish this form, so being
  // told that first is the difference between a hint and a dead end.
  await expect(page.getByText(/setup token/i).first()).toBeVisible();
  await expect(page.getByText(/docker compose logs/i).first()).toBeVisible();
  // The cursor starts at the one field they have to go and fetch.
  await expect(page.locator('input[name="setup-token"]')).toBeFocused();
});

/**
 * The phone rules, on one page.
 *
 * Bootstrap and sign-in are outside the app shell -- no sidebar, no top bar,
 * their own card layout -- so every phone-width rule the mobile project checks
 * on the shell has never been checked on either of them. They are also the two
 * pages most likely to be opened on a phone: somebody finishing a
 * `docker compose up` from wherever they happen to be standing.
 */
async function expectPhoneSafe(
  page: import('@playwright/test').Page,
  where: string,
): Promise<void> {
  const { scrollWidth, clientWidth } = await page.evaluate(() => ({
    scrollWidth: document.documentElement.scrollWidth,
    clientWidth: document.documentElement.clientWidth,
  }));
  expect(
    scrollWidth,
    `${where} is ${scrollWidth}px wide in a ${clientWidth}px window, so the page scrolls sideways`,
  ).toBeLessThanOrEqual(clientWidth);

  // Mobile Safari zooms the viewport whenever a focused control's text is under
  // 16px, and the viewport meta sets no maximum-scale on purpose -- so a field
  // a pixel under the line throws the card off both edges the moment it is
  // tapped, which on these two pages is immediately.
  const small = await page.evaluate(() =>
    Array.from(document.querySelectorAll('input, select, textarea'))
      .filter((el) => {
        const type = el.getAttribute('type');
        return type !== 'checkbox' && type !== 'radio' && type !== 'hidden';
      })
      .map((el) => ({
        name: el.getAttribute('name') ?? el.getAttribute('type') ?? el.tagName,
        size: Number.parseFloat(getComputedStyle(el).fontSize),
      }))
      .filter((f) => f.size < 16),
  );
  expect(small, `${where} has controls whose text is under 16px`).toEqual([]);
}

test('the first screen fits a phone', async ({ page }) => {
  await page.setViewportSize({ width: 360, height: 780 });
  await page.goto('/');
  await expect(page.getByRole('heading', { name: 'Create the first account' })).toBeVisible();
  await expectPhoneSafe(page, 'the bootstrap page');
});

/**
 * The way through the first screen is on the first screen.
 *
 * The card used to open with a 298px black logo tile and close with a fifth
 * field -- an optional email that nothing in Zoomies ever sends mail to -- so
 * on a 1440x900 laptop the button that creates the account sat 270px below the
 * fold, and the one thing in view was the brand. An operator who has just run
 * `docker compose up` is seconds from the product; the control that gets them
 * there belongs on the screen they are already looking at.
 */
test('the button that creates the account is on screen on a laptop', async ({ page }) => {
  await page.setViewportSize({ width: 1440, height: 900 });
  await page.goto('/');
  const submit = page.getByRole('button', { name: 'Create the account' });
  await expect(submit).toBeVisible();
  const box = await submit.boundingBox();
  expect(box, 'the submit button has a box').not.toBeNull();
  expect(
    box!.y + box!.height,
    'the submit button ends below the fold of a 1440x900 window',
  ).toBeLessThanOrEqual(900);

  // Nor is there a field nobody uses to push it back down.
  await expect(page.getByLabel('Email')).toHaveCount(0);
});

test('the setup token says where to find it for every way of running Zoomies', async ({ page }) => {
  await page.goto('/');
  const token = page.locator('input[name="setup-token"]');
  // The visible hint is the Compose command, because that is what most people
  // ran. Everyone else -- a container they started, a systemd unit, a PaaS --
  // used to be left to guess where "the controller's log" is, and a form that
  // cannot be finished without it is a dead end for them. The sentence is in
  // the accessible description at all times, not only in the help bubble.
  await expect(token).toHaveAccessibleDescription(/zoomies logs/);
  await expect(token).toHaveAccessibleDescription(/docker logs/);
  await expect(token).toHaveAccessibleDescription(/journalctl -u zoomies/);
});

test('submitting an empty form moves focus to the field that is missing', async ({ page }) => {
  await page.goto('/');
  await expect(page.locator('input[name="setup-token"]')).toBeFocused();

  // Pressing Enter on an empty form used to do nothing visible at all: the
  // focus jump queried the DOM for aria-invalid before Svelte had rendered it,
  // so it matched nothing and focus stayed on the button.
  await page.keyboard.press('Enter');

  await expect(page.getByText('Paste the setup token from the controller log.')).toBeVisible();
  await expect(page.locator('input[name="setup-token"]')).toBeFocused();

  // With that filled in, the next missing field is the one focus moves to.
  await page.fill('input[name="setup-token"]', setupToken());
  await page.keyboard.press('Enter');
  await expect(page.getByText('Choose a username for the first account.')).toBeVisible();
  await expect(page.locator('input[name="username"]')).toBeFocused();
});

test('an error replaces the hint rather than pushing the button out from under the pointer', async ({
  page,
}) => {
  await page.goto('/');
  const submit = page.getByRole('button', { name: 'Create the account' });

  // Measured after the focus, not before it: the card is taller than the
  // viewport, so focusing a field part-way down scrolls the page, and that
  // scroll is not what this test is about. What it is about is the next line.
  await page.locator('input[name="username"]').focus();
  const before = await submit.boundingBox();

  // Blurring an empty required field shows its error. When that error was
  // rendered *alongside* the hint it added a row, the button moved 26px down
  // between mousedown and mouseup, and the click was delivered to whatever
  // took its place -- a submit button that visibly did nothing.
  await page.locator('input[name="username"]').blur();
  await expect(page.getByText('Choose a username for the first account.')).toBeVisible();

  expect((await submit.boundingBox())?.y).toBe(before?.y);
});

test('a short password is refused with the counter still visible', async ({ page }) => {
  await page.goto('/');
  await page.fill('input[name="setup-token"]', setupToken());
  await page.fill('input[name="username"]', ADMIN.username);
  await page.fill('input[name="password"]', 'short');
  await page.getByRole('button', { name: 'Create the account' }).click();

  // The error is the counter: it says how many characters are still needed,
  // which is more use than the strength hint it replaces.
  await expect(page.getByText('A few more characters: 7 to go.')).toBeVisible();
  await expect(page.locator('input[name="password"]')).toBeFocused();
});

test('a wrong setup token is refused, and nobody is created', async ({ page }) => {
  await page.goto('/');
  await page.fill('input[name="setup-token"]', 'zoo-not-the-setup-token');
  await page.fill('input[name="username"]', ADMIN.username);
  await page.fill('input[name="password"]', ADMIN.password);
  await page.fill('input[name="confirm-password"]', ADMIN.password);
  await page.getByRole('button', { name: 'Create the account' }).click();

  // The refusal lands on the field it is about and says where the real one is.
  await expect(page.getByText(/is not this controller's setup token/i).first()).toBeVisible();
  // Still on the first-run form: the route has not closed, so the operator who
  // does have the token can still use it. The test after this one does.
  await expect(page.getByRole('heading', { name: 'Create the first account' })).toBeVisible();
});

test('creating the administrator lands somewhere that names the next step', async ({ page }) => {
  await page.goto('/');
  await page.fill('input[name="setup-token"]', setupToken());
  await page.fill('input[name="username"]', ADMIN.username);
  await page.fill('input[name="password"]', ADMIN.password);
  await page.fill('input[name="confirm-password"]', ADMIN.password);
  await page.getByRole('button', { name: 'Create the account' }).click();

  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();
  // The whole point of the checklist: the first action offered is the one that
  // can actually be completed, and it is not "Create a pool".
  const checklist = page.getByRole('region', { name: 'Finish setting up' });
  await expect(checklist).toBeVisible();
  await expect(checklist.getByRole('link', { name: /Connect GitHub/ })).toBeVisible();
  // A pool is impossible without an installation, so it is not offered as one.
  await expect(checklist.getByText('After GitHub is connected.')).toBeVisible();
});

test('a fleet that has done nothing says so in dashes, and blames no filter', async ({ page }) => {
  await signIn(page);

  // The controller answers 0 for a median it has no samples for, and the tiles
  // used to print it: "0ms" is a claim -- instant -- about a fleet that has not
  // started a single runner, and it was the first thing a new operator read in
  // the row meant to judge the fleet by. "--" is what every other empty value
  // in the product says.
  for (const name of [/^Median queue wait/, /^Runner startup/, /^Registration/]) {
    const tile = page.getByRole('link', { name });
    await expect(tile).toBeVisible();
    await expect(tile).toContainText('--');
    await expect(tile).not.toContainText(/\b0\s?ms\b/);
  }

  // Two kinds of event are off by default, and a fleet that has done nothing
  // has nothing in any kind. The empty panel used to blame the two switches
  // nobody had touched and send the operator to Settings for a problem that
  // was not there.
  const feed = page.getByRole('region', { name: 'Recent events', exact: true });
  await expect(feed).toContainText('Nothing has happened yet');
  await expect(feed).not.toContainText('switched off for this browser');
});

test('signing out and back in reports the three kinds of failure differently', async ({ page }) => {
  await page.goto('/');
  await page.context().clearCookies();
  await page.goto('/login');

  await expect(page.getByRole('heading', { name: 'Sign in', level: 1 })).toBeVisible();
  // The brand panel is where somebody who did not install this controller
  // learns what it is, where its source lives and who makes it. Every link
  // leaves the page for another site, so every one opens a new tab and says
  // so to a screen reader -- hence the names are matched from the start rather
  // than exactly, the way the shell footer's are.
  const about = page.getByRole('navigation', { name: 'About Zoomies' });
  for (const [name, href] of [
    [/^zoomies\.sh\b/, 'https://zoomies.sh'],
    [/^GitHub\b/, 'https://github.com/eyupio/zoomies'],
    [/^EyUp\.io\b/, 'https://eyup.io'],
  ] as const) {
    const link = about.getByRole('link', { name });
    await expect(link).toHaveAttribute('href', href);
    await expect(link).toHaveAttribute('target', '_blank');
    await expect(link).toHaveAttribute('rel', /noopener/);
    await expect(link).toHaveAccessibleName(/\(opens in a new tab\)$/);
  }
  await expect(page.getByText(/Developed by/)).toBeVisible();

  await page.fill('input[name="username"]', ADMIN.username);
  await page.fill('input[name="password"]', 'not the password');
  await page.getByRole('button', { name: 'Sign in' }).click();

  // The server's own words, read as a sentence: it sends them lowercase and
  // unpunctuated, because they are also read in a log line.
  const alert = page.getByRole('alert');
  await expect(alert).toContainText('Incorrect username or password.');
  // The password is cleared and the cursor put back in it: retyping is the
  // next thing to do whichever failure this was.
  await expect(page.locator('input[name="password"]')).toBeFocused();
  await expect(page.locator('input[name="password"]')).toHaveValue('');

  await page.fill('input[name="password"]', ADMIN.password);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();
});

test('the checklist button opens the connect dialog instead of a page that asks again', async ({
  page,
}) => {
  await signIn(page);
  const checklist = page.getByRole('region', { name: 'Finish setting up' });
  await checklist.getByRole('link', { name: /Connect GitHub/ }).click();

  await expect(page.getByRole('dialog', { name: 'Connect GitHub' })).toBeVisible();
  // `connect=1` is an instruction, not a place: it is out of the address bar
  // as soon as it has been followed, so a reload does not reopen what was closed.
  await expect(page).toHaveURL(/\/installations$/);
});

test('a page that needs GitHub first says so, neutrally, with the button', async ({ page }) => {
  await signIn(page);

  // The pool wizard used to find this out on its second step, after a click
  // through the first and four paragraphs of explainer.
  await page.goto('/pools/new');
  await expect(page.getByRole('heading', { name: 'Create a pool', level: 1 })).toBeVisible();
  await expect(page.getByText('Connect GitHub first')).toBeVisible();
  await expect(page.getByRole('radio')).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Connect GitHub' })).toHaveAttribute(
    'href',
    '/installations?connect=1',
  );

  // And the migration wizard said it in red, as an alert, with the instruction
  // as plain text. Red means failure here; nothing has failed.
  await page.goto('/migrate');
  await expect(page.getByText('Connect GitHub to migrate')).toBeVisible();
  await expect(page.getByRole('alert')).toHaveCount(0);
  await expect(page.getByRole('link', { name: 'Connect GitHub' })).toHaveAttribute(
    'href',
    '/installations?connect=1',
  );

  // Nothing has been connected, so nothing can have failed to deliver a webhook.
  await page.goto('/installations');
  await expect(page.getByText('No GitHub connection yet')).toBeVisible();
  await expect(page.getByText(/No webhook has been received/)).toHaveCount(0);
});

test('the connect dialog refuses before the form when GitHub cannot reach here', async ({
  page,
}) => {
  await signIn(page);
  await page.goto('/installations');
  await page.getByRole('button', { name: 'Connect GitHub' }).first().click();

  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();
  // This fixture has no external URL, which is the state a fresh compose
  // deployment is in. The refusal used to come after the whole form had been
  // filled in, attached to the "Organisation" field.
  await expect(dialog.getByText('Zoomies has no external URL yet')).toBeVisible();
  await expect(dialog.getByRole('button', { name: 'Continue', exact: true })).toBeDisabled();
  // The way out is a link to the setting, not the name of a configuration key
  // to go and find. And there is no "create it anyway": with no address at all
  // there is no webhook URL to give GitHub, and the server would refuse the
  // manifest, so offering a tick that cannot work would be a lie.
  await expect(dialog.getByRole('link', { name: 'Open the setting' })).toHaveAttribute(
    'href',
    '/settings/configuration?setting=server.external_url',
  );
  await expect(dialog.getByLabel('Create the App anyway')).toHaveCount(0);
});

test('a private address can still be connected, once the operator says so', async ({ page }) => {
  await signIn(page);
  // The state an evaluation on a laptop, or a home lab, is in: Zoomies is
  // reached at an address only this machine has. The fixture's own controller
  // has none, so the browser is told the one install.sh's "local-only" answer
  // produces. The guard is the dialog's alone -- the server only refuses an
  // empty address -- which is why this needs no second controller.
  await page.route('**/api/v1/meta', async (route) => {
    const response = await route.fetch();
    const meta = (await response.json()) as Record<string, unknown>;
    await route.fulfill({
      response,
      json: {
        ...meta,
        external_url: 'http://localhost:8080',
        webhook_url: 'http://localhost:8080/webhooks/github',
      },
    });
  });
  await page.goto('/installations');
  await page.getByRole('button', { name: 'Connect GitHub' }).first().click();

  const dialog = page.getByRole('dialog');
  const proceed = dialog.getByRole('button', { name: 'Continue', exact: true });
  await expect(dialog.getByText('GitHub cannot reach Zoomies')).toBeVisible();
  await dialog.getByLabel('Organisation login').fill('acme');
  // Refused until it is asked for: the address is fixed in the App for good.
  await expect(proceed).toBeDisabled();
  await expect(dialog.getByText('What GitHub will be told')).toBeHidden();

  // The three ways forward the terminal installer offers, in the browser.
  await expect(dialog.getByRole('link', { name: 'Open the setting' })).toHaveAttribute(
    'href',
    '/settings/configuration?setting=server.external_url',
  );
  const tunnel = dialog.getByRole('link', { name: /No public address\? Use a tunnel/ });
  await expect(tunnel).toHaveAttribute(
    'href',
    'https://zoomies.sh/home-lab/#where-the-controller-goes',
  );
  await expect(tunnel).toHaveAttribute('target', '_blank');
  await expect(tunnel).toHaveAttribute('rel', /noopener/);

  // Choosing to go on shows exactly what will be baked into the App.
  const anyway = dialog.getByLabel('Create the App anyway');
  await anyway.check();
  await expect(dialog.getByText('http://localhost:8080/webhooks/github')).toBeVisible();
  await expect(proceed).toBeEnabled();
  // And it is a choice that can be taken back.
  await anyway.uncheck();
  await expect(proceed).toBeDisabled();
  await anyway.check();

  await page.route('**/api/v1/installations/manifest', (route) =>
    route.fulfill({
      json: {
        post_url: 'https://github.com/organizations/acme/settings/apps/new',
        manifest: '{}',
        state: 'state-from-the-test',
      },
    }),
  );
  const asked = page.waitForRequest('**/api/v1/installations/manifest');
  await proceed.click();
  expect((await asked).postDataJSON()).toMatchObject({ target: 'acme', target_type: 'org' });
  // On to the step that sends the operator to GitHub.
  await expect(dialog.getByRole('button', { name: 'Create the App on GitHub' })).toBeVisible();
});

test('Enter in the change-password dialog submits it', async ({ page }) => {
  // Signed in with authentication on is the only place this dialog exists.
  // The password is changed and put straight back, so what follows still
  // signs in with the account the suite made.
  await signIn(page);
  await page.goto('/settings/account');
  const change = async (from: string, to: string) => {
    await page.getByRole('button', { name: 'Change password' }).first().click();
    const dialog = page.getByRole('dialog', { name: 'Change your password' });
    await dialog.getByLabel('Current password').fill(from);
    await dialog.getByLabel('New password', { exact: true }).fill(to);
    await dialog.getByLabel('New password again').fill(to);
    await dialog.getByLabel('New password again').press('Enter');
    await expect(dialog).toBeHidden();
  };
  const other = 'a different long passphrase';
  await change(ADMIN.password, other);
  await change(other, ADMIN.password);
});

test('the page behind the connect dialog is inert while it is open', async ({ page }) => {
  await signIn(page);
  await page.goto('/installations');
  await page.getByRole('button', { name: 'Connect GitHub' }).first().click();

  const dialog = page.getByRole('dialog');
  await expect(dialog).toBeVisible();

  // The page behind a modal is inert, so a screen reader's virtual cursor
  // cannot read out of the dialog into the navigation underneath -- a focus
  // trap stops Tab and says nothing to the virtual cursor.
  const navInert = () =>
    page.evaluate(
      () => document.querySelector<HTMLElement>('nav[aria-label="Sections"]')?.inert === true,
    );
  expect(await navInert()).toBe(true);
  // The toaster is exempt: a toast raised while a dialog is open is usually
  // about the dialog, and a live region nobody can hear is not one.
  expect(
    await page.evaluate(() => document.querySelector<HTMLElement>('.toaster')?.inert === true),
  ).toBe(false);

  await page.keyboard.press('Escape');
  await expect(dialog).toBeHidden();
  expect(await navInert()).toBe(false);
});

test('the sign-in form comes before the brand panel for a keyboard', async ({ page }) => {
  await page.goto('/login');
  const username = page.locator('input[name="username"]');
  await expect(username).toBeFocused();

  // The panel is drawn on the left, but its links are read after the form: a
  // keyboard going backwards from the first field meets the skip link, not
  // three links to other websites, and going forwards meets the password.
  await page.keyboard.press('Shift+Tab');
  await expect(page.getByRole('link', { name: 'Skip to the main content' })).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(username).toBeFocused();
  await page.keyboard.press('Tab');
  await expect(page.locator('input[name="password"]')).toBeFocused();
});

test('the brand panel is Zoomies Black in both themes', async ({ page }) => {
  // The panel is the brand's own ground, not a surface, so the theme does not
  // reach it -- and neither may it reach the text on it. A panel that followed
  // the light theme's muted grey would be 3:1 on black; one that followed the
  // surface colour would put white text on white.
  const read = () =>
    page.getByText('GitHub Actions runners on machines you own.').evaluate((tagline) => {
      let ground: Element | null = tagline;
      while (ground && getComputedStyle(ground).backgroundColor === 'rgba(0, 0, 0, 0)') {
        ground = ground.parentElement;
      }
      return {
        text: getComputedStyle(tagline).color,
        ground: ground ? getComputedStyle(ground).backgroundColor : '',
      };
    });
  for (const colorScheme of ['light', 'dark'] as const) {
    await page.emulateMedia({ colorScheme });
    await page.goto('/login');
    expect(await read(), `the panel in the ${colorScheme} theme`).toEqual({
      text: 'rgb(255, 255, 255)',
      ground: 'rgb(8, 8, 8)',
    });
  }
  // The lockup is on the panel and named, and the decoration is not.
  await expect(page.getByRole('img', { name: 'Zoomies' })).toBeVisible();
  expect(
    await page.evaluate(() =>
      Array.from(document.querySelectorAll('svg.rings')).every(
        (svg) => svg.getAttribute('aria-hidden') === 'true',
      ),
    ),
  ).toBe(true);
  // Which instance this is, as the browser reached it.
  await expect(page.getByText(new URL(page.url()).host, { exact: true })).toBeVisible();
});

test('the sign-in page fits a phone too', async ({ page }) => {
  // The controls are the touch size and the form can be worked through to the
  // button without anything running off the edge, whichever way up the phone
  // is held. Nothing is focused on arrival: a focused field opens the keyboard
  // over half the page before the person has chosen where to type.
  const workable = async (where: string) => {
    await expectPhoneSafe(page, where);
    await expect(page.locator('input[name="username"]')).not.toBeFocused();
    for (const name of ['username', 'password']) {
      const box = await page.locator(`input[name="${name}"]`).boundingBox();
      expect(
        box?.height,
        `${where}: the ${name} field is under the touch size`,
      ).toBeGreaterThanOrEqual(44);
    }
    await page.locator('input[name="username"]').fill('mobile-user');
    await page.locator('input[name="password"]').fill('test password');
    await page.getByRole('button', { name: 'Show password' }).click();
    await expect(page.locator('input[name="password"]')).toHaveAttribute('type', 'text');
    const submit = page.getByRole('button', { name: 'Sign in', exact: true });
    await submit.scrollIntoViewIfNeeded();
    await expect(submit).toBeInViewport();
  };

  // Upright: the smallest phone still in service, and a current one.
  for (const viewport of [
    { width: 320, height: 568 },
    { width: 390, height: 844 },
  ]) {
    await page.setViewportSize(viewport);
    await page.goto('/login');
    const where = `the sign-in page at ${viewport.width}x${viewport.height}`;
    await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();

    // The panel comes apart: the lockup stays above the form, at the brand's
    // minimum rather than shrunk to a chip, and the heading is still above the
    // fold under it. On a current phone the button is on screen as well, so
    // signing in never needs a scroll. The links move below the form rather
    // than being dropped -- they are the page's only answer to "what is this"
    // -- and the pitch goes.
    const lockup = await page.getByRole('img', { name: 'Zoomies', exact: true }).boundingBox();
    const heading = await page.getByRole('heading', { level: 1 }).boundingBox();
    expect(lockup?.width, `${where}: the lockup is under the brand minimum`).toBeGreaterThanOrEqual(
      220,
    );
    expect((lockup?.y ?? 0) < (heading?.y ?? 0), `${where}: the lockup is above the form`).toBe(
      true,
    );
    expect(
      heading && heading.y + heading.height,
      `${where}: the heading is below the fold`,
    ).toBeLessThan(viewport.height);
    if (viewport.height >= 800) {
      await expect(page.getByRole('button', { name: 'Sign in', exact: true })).toBeInViewport();
    }
    const about = page.getByRole('navigation', { name: 'About Zoomies' });
    for (const name of [/^zoomies\.sh\b/, /^GitHub\b/, /^EyUp\.io\b/]) {
      await expect(about.getByRole('link', { name })).toBeVisible();
    }
    await expect(page.getByText('GitHub Actions runners on machines you own.')).toBeHidden();
    await workable(where);
  }

  // On its side, a phone is wider than the band layout and keeps the desktop
  // split, panel beside form; it is still a phone, so the same controls apply.
  await page.setViewportSize({ width: 844, height: 390 });
  await page.goto('/login');
  await expect(page.getByRole('heading', { name: 'Sign in', exact: true })).toBeVisible();
  await workable('the sign-in page at 844x390');
});

/*
 * Two audiences for one instance.
 *
 * The bootstrap account above holds the platform role: it is whoever runs the
 * process. Everybody after it -- viewer, operator, administrator -- is the
 * fleet, and must not be shown where the process listens, where it keeps its
 * database and key, or what its heap and goroutines are doing: not on a page,
 * not in a response the page makes, not in a frame of the event stream. The
 * API tests pin each route; these pin the whole of what a browser signed in as
 * each role receives, which is where a route nobody thought to test shows up.
 * The audit frame and the bootstrap keys were both found that way.
 *
 * They run here, after the bootstrap, because only this server has
 * authentication on and an account with the platform role. The needles are
 * read from the platform's own settings rather than written down, so a
 * fixture that moves its temporary directory is still scanned for.
 */

type Page = import('@playwright/test').Page;
type ApiContext = import('@playwright/test').APIRequestContext;
type ApiResponse = import('@playwright/test').APIResponse;

const FLEET_ROLES = ['viewer', 'operator', 'admin'] as const;
const PASSWORD = ADMIN.password;
/** Set by the platform while each role watches, so the stream has a frame to carry. */
const BACKUP_DIRECTORY = '/srv/zoomies-admin-backups';

/** What the platform is told and nobody else is. */
interface PlatformFacts {
  bind: string;
  keyFile: string;
  /** The database's directory: the key and the runners' work directory sit under it too. */
  stateDir: string;
  databasePath: string;
}

/** A signed-in API client. Same-origin, because a session cookie is refused without it. */
async function apiAs(
  request: typeof import('@playwright/test').request,
  baseURL: string,
  username: string,
): Promise<ApiContext> {
  const api = await request.newContext({
    baseURL,
    extraHTTPHeaders: { Origin: baseURL },
  });
  const login = await api.post('/api/v1/auth/login', { data: { username, password: PASSWORD } });
  expect(login.status(), `signing ${username} in`).toBe(200);
  return api;
}

async function platformFacts(platform: ApiContext): Promise<PlatformFacts> {
  const res = await platform.get('/api/v1/settings');
  expect(res.status()).toBe(200);
  const body = await res.json();
  const databasePath = String(body.database_path);
  const facts: PlatformFacts = {
    bind: body.config.server.bind,
    keyFile: body.config.security.encryption_key_file,
    stateDir: databasePath.replace(/\/[^/]+$/, ''),
    databasePath,
  };
  // An empty needle is in every string and so proves nothing.
  for (const [name, value] of Object.entries(facts)) {
    expect(value, `the platform's settings name no ${name}`).toMatch(/\S{4,}/);
  }
  return facts;
}

/**
 * What in `text` the fleet must not see, named for the failure message.
 *
 * The state directory stands for every path under it, so a new path setting
 * is caught without a new needle. The process figures are matched as non-zero
 * values, because a fleet's bundle keeps those fields and zeroes them.
 */
function leaks(text: string, facts: PlatformFacts): string[] {
  const found: string[] = [];
  if (text.includes(facts.bind)) found.push(`the bind address ${facts.bind}`);
  if (text.includes(facts.stateDir)) found.push(`a path under ${facts.stateDir}`);
  if (text.includes(facts.keyFile)) found.push(`the key file ${facts.keyFile}`);
  if (text.includes(BACKUP_DIRECTORY)) found.push(`the backup directory ${BACKUP_DIRECTORY}`);
  for (const figure of ['goroutines', 'heap_in_use_bytes', 'event_subscribers']) {
    if (new RegExp(`"${figure}"\\s*:\\s*[1-9]`).test(text)) found.push(`the process's ${figure}`);
  }
  return found;
}

/** Every /api/ response a page receives, kept as text for the scan. */
function recordResponses(page: Page): Array<{ url: string; body: string }> {
  const seen: Array<{ url: string; body: string }> = [];
  page.on('response', async (res) => {
    const url = res.url();
    if (!url.includes('/api/')) return;
    // A stream never finishes, so its body never arrives; streamWhile reads it.
    if ((res.headers()['content-type'] ?? '').includes('text/event-stream')) return;
    try {
      seen.push({ url, body: await res.text() });
    } catch {
      /* a response the page navigated away from has no body left to read */
    }
  });
  return seen;
}

/**
 * Hold the event stream open in the page while `during` runs, and return what
 * it carried. In the page rather than from Node, so the frames are the ones
 * this browser's session is sent.
 */
async function streamWhile(page: Page, during: () => Promise<void>): Promise<string> {
  const reading = page.evaluate(async () => {
    const res = await fetch('/api/v1/events');
    const reader = res.body!.getReader();
    const decoder = new TextDecoder();
    let text = '';
    const until = Date.now() + 4_000;
    while (Date.now() < until) {
      const next = await Promise.race([
        reader.read(),
        new Promise<null>((resolve) => setTimeout(() => resolve(null), until - Date.now())),
      ]);
      if (!next || next.done) break;
      text += decoder.decode(next.value);
    }
    await reader.cancel();
    return text;
  });
  // Long enough for the page's request to have subscribed before anything moves.
  await page.waitForTimeout(500);
  await during();
  return reading;
}

async function signInAs(page: Page, username: string): Promise<void> {
  await page.goto('/login');
  await page.fill('input[name="username"]', username);
  await page.fill('input[name="password"]', PASSWORD);
  await page.getByRole('button', { name: 'Sign in' }).click();
  await expect(page.getByRole('heading', { name: 'Overview', level: 1 })).toBeVisible();
}

/** Every page a role might look for the machine on, then the problems drawer. */
async function tour(page: Page, facts: PlatformFacts, who: string): Promise<void> {
  for (const path of [
    '/',
    '/hosts',
    '/settings/configuration',
    '/settings/tokens',
    '/settings/backups',
    '/settings/about',
    '/audit',
  ]) {
    await page.goto(path);
    await expect(page.getByRole('heading', { level: 1 })).toBeVisible();
    await page.waitForLoadState('networkidle');
    expect(leaks(await page.locator('body').innerText(), facts), `${who} on ${path}`).toEqual([]);
  }
  await page.getByRole('button', { name: /^Problems\./ }).click();
  const drawer = page.getByRole('dialog', { name: 'Problems' });
  await expect(drawer).toBeVisible();
  expect(leaks(await drawer.innerText(), facts), `${who} in the problems drawer`).toEqual([]);
  await page.keyboard.press('Escape');
}

test.describe('what each role is shown', () => {
  // Each walks seven pages and holds a stream open; the default is for one page.
  test.describe.configure({ timeout: 60_000 });
  let platform: ApiContext;
  let facts: PlatformFacts;
  /** A token the platform owns, so there is one for the fleet not to be listed. */
  let platformToken: string;

  test.beforeAll(async ({ playwright }, info) => {
    platform = await apiAs(playwright.request, info.project.use.baseURL!, ADMIN.username);
    // The premise: the account that bootstrapped the instance runs it.
    expect((await (await platform.get('/api/v1/auth/session')).json()).role).toBe('platform');
    facts = await platformFacts(platform);
    for (const role of FLEET_ROLES) {
      const made = await platform.post('/api/v1/users', {
        data: { username: `fleet-${role}`, password: PASSWORD, role },
      });
      expect(made.status(), `creating the ${role}`).toBe(201);
    }
    const token = await platform.post('/api/v1/tokens', {
      data: { name: 'the platform’s automation', role: 'platform' },
    });
    expect(token.status()).toBe(201);
    platformToken = (await token.json()).id;
  });

  test.afterAll(async () => {
    await platform?.dispose();
  });

  for (const role of FLEET_ROLES) {
    test(`a fleet ${role} is shown nothing of the machine the process runs on`, async ({
      page,
      playwright,
    }, info) => {
      const username = `fleet-${role}`;
      const responses = recordResponses(page);
      await signInAs(page, username);

      // A platform setting changed while this role watches: the frame that
      // records the change is the one most likely to carry the value along.
      const frames = await streamWhile(page, async () => {
        const changed = await platform.patch('/api/v1/settings', {
          data: { 'backup.directory': `${BACKUP_DIRECTORY}-${role}` },
        });
        expect(changed.status()).toBe(200);
      });
      expect(frames, 'the stream carried the audit frame for that change').toContain(
        'event: audit',
      );
      expect(leaks(frames, facts), `${role}'s event stream`).toEqual([]);

      await tour(page, facts, role);
      expect(responses.length, 'the pages made requests to scan').toBeGreaterThan(5);
      for (const { url, body } of responses) {
        expect(leaks(body, facts), `${role} was sent this by ${url}`).toEqual([]);
      }

      // The platform's page is listed, locked, with the reason -- not hidden.
      await page.goto('/settings/backups');
      await expect(
        page.getByText('Backups needs the platform role', { exact: true }),
      ).toBeVisible();
      await expect(page.getByText(/belongs to whoever runs this controller/)).toBeVisible();

      // And every platform-only act is refused by the API, not only the page.
      const api = await apiAs(playwright.request, info.project.use.baseURL!, username);
      try {
        const refused: Array<[string, () => Promise<ApiResponse>]> = [
          [
            'change a platform-scoped key',
            () => api.patch('/api/v1/settings', { data: { 'backup.keep': 3 } }),
          ],
          ['lift the fence', () => api.post('/api/v1/recovery/unfence')],
          ['list the backups', () => api.get('/api/v1/backups')],
          ['run a restore', () => api.post('/api/v1/backups/bak_nonexistent/restore')],
          [
            'make a platform account',
            () =>
              api.post('/api/v1/users', {
                data: { username: `${role}-shadow`, password: PASSWORD, role: 'platform' },
              }),
          ],
        ];
        for (const [what, act] of refused) {
          // An administrator's refusal of a platform key is a 422 that says
          // whose key it is; everything else never reaches its handler.
          expect([403, 422], `a ${role} may not ${what}`).toContain((await act()).status());
        }

        const bundle = await api.get('/api/v1/diagnostics/bundle');
        const tokens = await api.get('/api/v1/tokens');
        if (role === 'admin') {
          // An administrator takes the fleet's half of the bundle, and is
          // not listed the platform's own tokens.
          expect(bundle.status()).toBe(200);
          expect(leaks(await bundle.text(), facts), 'the administrator’s bundle').toEqual([]);
          expect(tokens.status()).toBe(200);
          expect(await tokens.text()).not.toContain(platformToken);
        } else {
          expect(bundle.status()).toBe(403);
          // Anybody signed in lists their own tokens, and only those.
          expect(tokens.status()).toBe(200);
          expect(await tokens.text()).not.toContain(platformToken);
        }
      } finally {
        await api.dispose();
      }
    });
  }

  // The other side of the line. A scan that passed because every page was
  // blank would prove nothing, so the platform is shown, through the same
  // helpers, each thing the fleet was not -- and may do each thing it may not.
  test('the platform is shown the machine, and may do what the fleet may not', async ({ page }) => {
    const responses = recordResponses(page);
    await signInAs(page, ADMIN.username);

    const frames = await streamWhile(page, async () => {
      const changed = await platform.patch('/api/v1/settings', {
        data: { 'backup.directory': `${BACKUP_DIRECTORY}-platform` },
      });
      expect(changed.status()).toBe(200);
    });
    expect(leaks(frames, facts)).toContain(`the backup directory ${BACKUP_DIRECTORY}`);

    await page.goto('/settings/configuration');
    await expect(page.getByText(facts.databasePath).first()).toBeVisible();
    await page.goto('/settings/backups');
    await expect(page.getByRole('heading', { name: 'Backups', level: 1 })).toBeVisible();
    await expect(page.getByText('Backups needs the platform role', { exact: true })).toHaveCount(0);
    await page.waitForLoadState('networkidle');
    expect(leaks(responses.map((r) => r.body).join('\n'), facts)).toEqual(
      expect.arrayContaining([`the bind address ${facts.bind}`, `a path under ${facts.stateDir}`]),
    );

    const bundle = await platform.get('/api/v1/diagnostics/bundle');
    expect(bundle.status()).toBe(200);
    expect(leaks(await bundle.text(), facts)).toEqual(
      expect.arrayContaining(["the process's goroutines", "the process's heap_in_use_bytes"]),
    );
    expect(await (await platform.get('/api/v1/tokens')).text()).toContain(platformToken);
    expect((await platform.get('/api/v1/backups')).status()).toBe(200);
    expect(
      (await platform.patch('/api/v1/settings', { data: { 'backup.keep': 8 } })).status(),
    ).toBe(200);
    // Nothing is fenced and there is no such backup, so neither of these has
    // anything to do -- but each is answered for what it names, not refused
    // for who asked.
    expect((await platform.post('/api/v1/recovery/unfence')).status()).not.toBe(403);
    expect((await platform.post('/api/v1/backups/bak_nonexistent/restore')).status()).not.toBe(403);
  });
});
