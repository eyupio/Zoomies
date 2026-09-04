// Boots a real `zoomies controller` for the Playwright suite.
//
// The tests drive the actual binary rather than a mock: authentication off (on
// loopback, which is the only place the config validator permits it), a
// throwaway SQLite database, and a seeded fixture set so the grids and the
// Overview have something to show. If the UI works here it works in production,
// because it is the same server.

import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

const port = process.argv[2] ?? '8099';
const root = resolve(import.meta.dirname, '..', '..', '..');
const binary = join(root, 'zoomies');

if (!existsSync(binary)) {
  console.error(
    `zoomies binary not found at ${binary}.\n` +
      `Build it first:  make build   (or: go build -o zoomies ./cmd/zoomies)`,
  );
  process.exit(1);
}

const dir = mkdtempSync(join(tmpdir(), 'zoomies-e2e-'));
const cleanup = () => {
  try {
    rmSync(dir, { recursive: true, force: true });
  } catch {
    /* the OS will get it */
  }
};

const child = spawn(binary, ['controller'], {
  stdio: 'inherit',
  env: {
    ...process.env,
    ZOOMIES_BIND: `127.0.0.1:${port}`,
    ZOOMIES_DISABLE_AUTH: 'true',
    ZOOMIES_DB_PATH: join(dir, 'zoomies.db'),
    ZOOMIES_STATE_DIR: dir,
    ZOOMIES_CONFIG_DIR: dir,
    ZOOMIES_WORK_DIR: join(dir, 'work'),
    // No agent: these tests are about the UI, and an embedded agent would try
    // to talk to a Docker daemon that CI does not guarantee.
    ZOOMIES_AGENT_EMBEDDED: 'false',
    ZOOMIES_POLL_FALLBACK: 'false',
    ZOOMIES_LOG_FORMAT: 'text',
    ZOOMIES_LOG_LEVEL: 'warn',
    // Seeds a deterministic fixture fleet so pages have content to assert on.
    ZOOMIES_SEED_DEMO: 'true',
  },
});

const stop = (signal) => {
  child.kill(signal);
  cleanup();
};
process.on('SIGINT', () => stop('SIGINT'));
process.on('SIGTERM', () => stop('SIGTERM'));
process.on('exit', cleanup);
child.on('exit', (code) => {
  cleanup();
  process.exit(code ?? 0);
});
