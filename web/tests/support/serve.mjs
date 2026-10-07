// The monitoring fixture: authentication off and a seeded demo fleet.
//
// Authentication is disabled on loopback, which is the only place the config
// validator permits it, so the grids and the Overview are reachable without a
// sign-in step in every spec. The seed is deterministic, so the fixtures in
// ./fixtures.ts can assert on exact counts.

import { serveController } from './controller.mjs';

serveController({
  port: process.argv[2] ?? '8099',
  prefix: 'zoomies-e2e-',
  env: {
    ZOOMIES_DISABLE_AUTH: 'true',
    // Seeds a deterministic fixture fleet so pages have content to assert on.
    ZOOMIES_SEED_DEMO: 'true',
    // Enrolment shares the sign-in limiter, ten attempts an address a minute, and every
    // request here comes from loopback. Both projects run their host specs one after the
    // other against this one controller, and a spec that joins a few hosts of its own
    // landed on the minute the last one had used up: a 429 that was nothing to do with
    // what it tests. Sign-in is off here, so there is nothing for the limit to protect;
    // the limiter itself is covered by the Go tests.
    ZOOMIES_RATE_LIMIT_LOGINS: '1000',
  },
});
