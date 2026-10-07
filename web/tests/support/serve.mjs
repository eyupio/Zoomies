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
    // Agent joins are limited per address per minute (ten by default), and every
    // spec here reaches the controller from the same loopback address. Specs that
    // each enrol a host to test its page add up to more than ten in a minute when
    // they run back to back, and the ones that fall past the tenth fail with a 429
    // that says nothing about what they test. internal/auth's own tests cover the
    // limiter, so this fixture only keeps it out of the way. Not zero: that turns
    // limiting off and raises auth.no_login_limit on a fleet meant to be clean.
    ZOOMIES_RATE_LIMIT_LOGINS: '200',
  },
});
