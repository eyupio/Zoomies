import { defineConfig, devices } from '@playwright/test';
import { SETUP_TOKEN_FILE } from './tests/support/fixtures';

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
/**
 * The diagnostics project gets its own controller too, and for the same kind
 * of reason: the shared fixture is deliberately a fleet with nothing wrong
 * with it, so the pages that explain a fleet in trouble have nothing to show
 * there. This one seeds the same fleet and then breaks three things in it.
 */
const STUCK_PORT = 8097;
/**
 * The connect project gets its own controller and its own GitHub.
 *
 * The suite's fake GitHub is in-process, so a browser cannot reach it: the
 * connect and verify pages were tested at every layer except the one they live
 * on. This fixture runs the same fake as a program on a loopback port, beside a
 * controller with nothing seeded, and leaves its address in a file the spec
 * reads.
 */
const CONNECT_PORT = 8096;
/**
 * The status project gets its own controller: authentication on, the
 * diagnostics fleet seeded, and status.mode public. The status page is for a
 * reader with no account, and under the shared server -- authentication off --
 * every request is an administrator, so "signed out" could not be tested.
 */
const STATUS_PORT = 8095;
const FAKE_GITHUB_FILE = 'test-results/fakegithub.json';

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
      testIgnore: /(first-run|diagnostics|connect|status-page)\.spec\.ts/,
    },
    // Read-only monitoring on a phone is a stated requirement, so it is tested.
    {
      name: 'mobile',
      use: { ...devices['Pixel 7'] },
      testIgnore: /(first-run|diagnostics|connect|status-page)\.spec\.ts/,
    },
    {
      name: 'first-run',
      testMatch: /first-run\.spec\.ts/,
      use: { ...devices['Desktop Chrome'], baseURL: `http://127.0.0.1:${FIRST_RUN_PORT}` },
    },
    {
      name: 'diagnostics',
      testMatch: /diagnostics\.spec\.ts/,
      use: { ...devices['Desktop Chrome'], baseURL: `http://127.0.0.1:${STUCK_PORT}` },
    },
    {
      name: 'status',
      testMatch: /status-page\.spec\.ts/,
      use: { ...devices['Desktop Chrome'], baseURL: `http://127.0.0.1:${STATUS_PORT}` },
    },
    {
      name: 'connect',
      testMatch: /connect\.spec\.ts/,
      use: { ...devices['Desktop Chrome'], baseURL: `http://127.0.0.1:${CONNECT_PORT}` },
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
      command: `node tests/support/serve-firstrun.mjs ${FIRST_RUN_PORT} ${SETUP_TOKEN_FILE}`,
      url: `http://127.0.0.1:${FIRST_RUN_PORT}/healthz`,
      // Never reused: the bootstrap route closes for ever once an account
      // exists, so this suite needs a database nobody has touched.
      reuseExistingServer: false,
      timeout: 60_000,
      stdout: 'pipe',
      stderr: 'pipe',
    },
    {
      command: `node tests/support/serve-connect.mjs ${CONNECT_PORT} ${FAKE_GITHUB_FILE}`,
      url: `http://127.0.0.1:${CONNECT_PORT}/healthz`,
      // Never reused: the spec connects an installation, and a database that
      // already has one answers the question the spec is asking.
      reuseExistingServer: false,
      timeout: 60_000,
      stdout: 'pipe',
      stderr: 'pipe',
    },
    {
      command: `node tests/support/serve-status.mjs ${STATUS_PORT}`,
      url: `http://127.0.0.1:${STATUS_PORT}/healthz`,
      reuseExistingServer: !process.env.CI,
      timeout: 60_000,
      stdout: 'pipe',
      stderr: 'pipe',
    },
    {
      command: `node tests/support/serve-stuck.mjs ${STUCK_PORT}`,
      url: `http://127.0.0.1:${STUCK_PORT}/healthz`,
      reuseExistingServer: !process.env.CI,
      timeout: 60_000,
      stdout: 'pipe',
      stderr: 'pipe',
    },
  ],
});
