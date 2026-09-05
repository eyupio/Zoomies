import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright drives the real binary, not a mock server: the tests boot
 * `zoomies controller` against a temporary SQLite database with auth disabled
 * on loopback, so what they exercise is the same code an operator runs.
 */
const PORT = 8099;
/**
 * The first-run project gets its own controller, because it needs the opposite
 * of what the others do: authentication on, and an empty fleet. `session.boot()`
 * short-circuits to `ready` when auth is disabled, so bootstrap and sign-in are
 * literally unreachable under the shared server.
 */
const FIRST_RUN_PORT = 8098;

export default defineConfig({
  testDir: './tests',
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  workers: 1,
  reporter: process.env.CI ? [['html'], ['github']] : 'list',
  timeout: 30_000,
  expect: { timeout: 7_000 },
  use: {
    baseURL: `http://127.0.0.1:${PORT}`,
    trace: 'retain-on-failure',
    screenshot: 'only-on-failure',
  },
  projects: [
    {
      name: 'chromium',
      use: { ...devices['Desktop Chrome'] },
      testIgnore: /first-run\.spec\.ts/,
    },
    // Read-only monitoring on a phone is a stated requirement, so it is tested.
    {
      name: 'mobile',
      use: { ...devices['Pixel 7'] },
      testIgnore: /first-run\.spec\.ts/,
    },
    {
      name: 'first-run',
      testMatch: /first-run\.spec\.ts/,
      use: { ...devices['Desktop Chrome'], baseURL: `http://127.0.0.1:${FIRST_RUN_PORT}` },
    },
  ],
  webServer: [
    {
      command: `node tests/support/serve.mjs ${PORT}`,
      url: `http://127.0.0.1:${PORT}/healthz`,
      reuseExistingServer: !process.env.CI,
      timeout: 60_000,
      stdout: 'pipe',
      stderr: 'pipe',
    },
    {
      command: `node tests/support/serve-firstrun.mjs ${FIRST_RUN_PORT}`,
      url: `http://127.0.0.1:${FIRST_RUN_PORT}/healthz`,
      // Never reused: the bootstrap route closes for ever once an account
      // exists, so this suite needs a database nobody has touched.
      reuseExistingServer: false,
      timeout: 60_000,
      stdout: 'pipe',
      stderr: 'pipe',
    },
  ],
});
