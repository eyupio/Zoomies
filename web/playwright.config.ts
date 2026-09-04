import { defineConfig, devices } from '@playwright/test';

/**
 * Playwright drives the real binary, not a mock server: the tests boot
 * `zoomies controller` against a temporary SQLite database with auth disabled
 * on loopback, so what they exercise is the same code an operator runs.
 */
const PORT = 8099;

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
    { name: 'chromium', use: { ...devices['Desktop Chrome'] } },
    // Read-only monitoring on a phone is a stated requirement, so it is tested.
    { name: 'mobile', use: { ...devices['Pixel 7'] } },
  ],
  webServer: {
    command: `node tests/support/serve.mjs ${PORT}`,
    url: `http://127.0.0.1:${PORT}/healthz`,
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
    stdout: 'pipe',
    stderr: 'pipe',
  },
});
