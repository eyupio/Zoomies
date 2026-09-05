// Boots a real `zoomies controller` the way an operator first meets it.
//
// The main harness (serve.mjs) runs with authentication off and a seeded demo
// fleet, which is exactly right for the monitoring pages and exactly wrong for
// the first-run ones: `session.boot()` short-circuits to `ready` on an
// auth-disabled instance, so the bootstrap card and the sign-in card are
// unreachable, and a seeded fleet is never an unconfigured one. Every defect
// the first-run review found lived in that blind spot.
//
// So: authentication on, an empty database, and no external URL -- the state a
// fresh `docker compose up` actually produces.

import { spawn } from 'node:child_process';
import { mkdtempSync, rmSync, existsSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join, resolve } from 'node:path';

const port = process.argv[2] ?? '8098';
const root = resolve(import.meta.dirname, '..', '..', '..');
const binary = join(root, 'zoomies');

if (!existsSync(binary)) {
  console.error(
    `zoomies binary not found at ${binary}.\n` +
      `Build it first:  make build   (or: go build -o zoomies ./cmd/zoomies)`,
  );
  process.exit(1);
}

const dir = mkdtempSync(join(tmpdir(), 'zoomies-firstrun-'));
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
    ZOOMIES_DB_PATH: join(dir, 'zoomies.db'),
    ZOOMIES_STATE_DIR: dir,
    ZOOMIES_CONFIG_DIR: dir,
    ZOOMIES_WORK_DIR: join(dir, 'work'),
    // Authentication stays ON: it is the whole point of this fixture.
    ZOOMIES_AGENT_EMBEDDED: 'false',
    ZOOMIES_POLL_FALLBACK: 'false',
    ZOOMIES_LOG_FORMAT: 'text',
    ZOOMIES_LOG_LEVEL: 'warn',
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
