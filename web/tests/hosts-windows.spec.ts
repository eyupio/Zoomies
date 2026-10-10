import { expect, test, type Page } from '@playwright/test';
import { browserOverride, goto } from './support/fixtures';

test.use(browserOverride);

const LINUX =
  "curl -fsSL https://zoomies.sh/install.sh | sh -s -- --mode agent --controller 'https://zoomies.example.com' --join-token 'zoojoin_fixture_only'";
const WINDOWS =
  "& ([scriptblock]::Create((irm https://zoomies.sh/install.ps1))) -Mode agent -Controller 'https://zoomies.example.com' -JoinToken 'zoojoin_fixture_only'";
const TAILCAT_WINDOWS =
  "& ([scriptblock]::Create((irm https://zoomies.sh/install.ps1))) -Mode agent -Controller 'tailcat://tc_fixture_only' -JoinToken 'zoojoin_fixture_only'";

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

/** The browser's own idea of its platform, which Playwright's desktop profile sets to Windows. */
async function setPlatform(page: Page, platform: string): Promise<void> {
  await page.addInitScript((value) => {
    Object.defineProperty(navigator, 'platform', { get: () => value });
    Object.defineProperty(navigator, 'userAgentData', { get: () => ({ platform: value }) });
  }, platform);
}

async function mockJoinToken(page: Page, windows: string, extra: Record<string, unknown> = {}) {
  await page.route('**/api/v1/join-tokens', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    await route.fulfill({
      status: 201,
      json: {
        id: 'join_windows_fixture',
        token: 'zoojoin_fixture_only',
        usable: true,
        expires_at: new Date(Date.now() + 900000).toISOString(),
        command: LINUX,
        commands: { linux: LINUX, windows },
        join_command:
          "zoomies agent join 'https://zoomies.example.com' --token 'zoojoin_fixture_only'",
        ...extra,
      },
    });
  });
  await page.route('**/api/v1/join-tokens/join_windows_fixture', (route) =>
    route.fulfill({
      json: {
        id: 'join_windows_fixture',
        usable: true,
        expires_at: new Date(Date.now() + 900000).toISOString(),
      },
    }),
  );
}

async function getTheCommand(page: Page) {
  await goto(page, '/hosts/new', 'Add a host');
  // An operator types the address the host will reach; the test origin is loopback,
  // whose warning sits over the submit button on a phone.
  await page
    .getByRole('textbox', { name: 'Controller address' })
    .fill('https://zoomies.example.com');
  await page.getByRole('button', { name: 'Get the command' }).click();
  await expect(page.getByRole('heading', { name: 'Run this on the new host' })).toBeFocused();
}

test('Linux stays the default away from Windows, and the Windows tab shows the PowerShell command', async ({
  page,
}) => {
  await stubClipboard(page);
  await setPlatform(page, 'MacIntel');
  await mockJoinToken(page, WINDOWS);
  await getTheCommand(page);

  await expect(page.getByRole('radio', { name: 'Linux or macOS' })).toBeChecked();
  await expect(page.locator('pre').filter({ hasText: 'zoomies.sh/install.sh' })).toBeVisible();
  await expect(page.getByText('Run this in PowerShell as administrator.')).toHaveCount(0);

  await page.getByRole('radio', { name: 'Windows' }).check({ force: true });
  const command = page.locator('pre').filter({ hasText: 'install.ps1' });
  await expect(command).toContainText(WINDOWS);
  await expect(command).not.toContainText('-Yes');
  await expect(page.getByText('Run this in PowerShell as administrator.')).toBeVisible();
  await expect(
    page.getByText(/Jobs run as processes on that machine, not in containers/),
  ).toBeVisible();
  await expect(
    page.getByText(/fresh work directory, but the machine keeps its state/),
  ).toBeVisible();
  await expect(page.getByText(/only for repositories you trust/)).toBeVisible();
  await expect(page.getByText(/Windows is not yet qualified/)).toBeVisible();
  await expect(page.getByRole('link', { name: 'support matrix' })).toHaveAttribute(
    'href',
    /zoomies\.sh\/#what-is-qualified/,
  );

  await page.getByRole('button', { name: 'Copy the PowerShell command' }).click();
  expect(await page.evaluate(() => sessionStorage.getItem('copied'))).toBe(WINDOWS);

  // Back to Linux: nothing about the Windows panel lingers.
  await page.getByRole('radio', { name: 'Linux or macOS' }).check({ force: true });
  await expect(page.getByText('Run this in PowerShell as administrator.')).toHaveCount(0);
  await page.getByRole('button', { name: 'Copy the install command' }).click();
  expect(await page.evaluate(() => sessionStorage.getItem('copied'))).toBe(LINUX);
});

test('a browser on Windows starts on the Windows command', async ({ page }) => {
  await setPlatform(page, 'Windows');
  await mockJoinToken(page, WINDOWS);
  await getTheCommand(page);
  await expect(page.getByRole('radio', { name: 'Windows' })).toBeChecked();
});

test('the private connection works with the Windows command', async ({ page }) => {
  await page.route('**/api/v1/meta', async (route) => {
    const response = await route.fetch();
    await route.fulfill({
      response,
      json: { ...(await response.json()), tailcat_available: true },
    });
  });
  await mockJoinToken(page, TAILCAT_WINDOWS);
  await goto(page, '/hosts/new', 'Add a host');
  await page.getByRole('radio', { name: /Private connection/ }).check({ force: true });
  await page.getByRole('button', { name: 'Get the command' }).click();
  await page.getByRole('radio', { name: 'Windows' }).check({ force: true });
  await expect(page.locator('pre').filter({ hasText: 'install.ps1' })).toContainText(
    'tailcat://tc_fixture_only',
  );
  await expect(
    page.getByText(/This command contains private connection credentials/),
  ).toBeVisible();
});

test('an older controller without a Windows command says so instead of showing nothing', async ({
  page,
}) => {
  await page.route('**/api/v1/join-tokens', async (route) => {
    if (route.request().method() !== 'POST') return route.continue();
    await route.fulfill({
      status: 201,
      json: {
        id: 'join_windows_fixture',
        token: 'zoojoin_fixture_only',
        usable: true,
        expires_at: new Date(Date.now() + 900000).toISOString(),
        command: LINUX,
      },
    });
  });
  await getTheCommand(page);
  await page.getByRole('radio', { name: 'Windows' }).check({ force: true });
  await expect(
    page.getByRole('alert').filter({ hasText: 'too old to offer a Windows command' }),
  ).toBeVisible();
});

for (const colorScheme of ['light', 'dark'] as const) {
  test(`the Windows panel fits a phone and reads in the ${colorScheme} theme`, async ({ page }) => {
    await page.emulateMedia({ colorScheme });
    await page.setViewportSize({ width: 360, height: 740 });
    await mockJoinToken(page, WINDOWS);
    await getTheCommand(page);
    await page.getByRole('radio', { name: 'Windows' }).check({ force: true });
    await expect(page.getByText('Run this in PowerShell as administrator.')).toBeVisible();
    // The page's own footer can be wider than a phone in some environments, so
    // the claim is about this panel: nothing in it reaches past the viewport.
    const widest = await page.evaluate(() =>
      Math.max(
        ...[...document.querySelectorAll('.windows-notes, .command, .os-options')].map(
          (el) => el.getBoundingClientRect().right,
        ),
      ),
    );
    expect(widest, 'the Windows panel fits the viewport').toBeLessThanOrEqual(360);
    // The OS choice is a keyboard control: arrow keys move between its two radios.
    await page.getByRole('radio', { name: 'Windows' }).focus();
    await page.keyboard.press('ArrowUp');
    await expect(page.getByRole('radio', { name: 'Linux or macOS' })).toBeChecked();
  });
}

test('a Windows host that joins is recognised, and the pool advice names the selector', async ({
  page,
}) => {
  await mockJoinToken(page, WINDOWS);
  await page.route('**/api/v1/join-tokens/join_windows_fixture', (route) =>
    route.fulfill({
      json: {
        id: 'join_windows_fixture',
        usable: false,
        used_at: new Date().toISOString(),
        used_by_id: 'host_win_fixture',
        expires_at: new Date(Date.now() + 900000).toISOString(),
      },
    }),
  );
  await page.route('**/api/v1/hosts/host_win_fixture', (route) =>
    route.fulfill({
      json: {
        id: 'host_win_fixture',
        name: 'win-builder',
        os: 'windows',
        arch: 'amd64',
        backends: ['process'],
        labels: {},
        capacity: 2,
        version: 'dev',
      },
    }),
  );
  await page.route(/\/api\/v1\/pools(?:\?.*)?$/, (route) =>
    route.fulfill({
      json: {
        items: [
          {
            id: 'pool_linux_fixture',
            name: 'linux-docker',
            backend: 'docker',
            enabled: true,
            host_selector: {},
          },
        ],
      },
    }),
  );
  await goto(page, '/hosts/new', 'Add a host');
  await page
    .getByRole('textbox', { name: 'Controller address' })
    .fill('https://zoomies.example.com');
  await page.getByRole('button', { name: 'Get the command' }).click();
  await expect(page.getByRole('heading', { name: 'win-builder joined' })).toBeVisible({
    timeout: 15000,
  });
  await expect(page.getByText(/This is a Windows host/)).toBeVisible();
  await expect(page.getByText('os=windows').first()).toBeVisible();
});
