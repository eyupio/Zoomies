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
    // The join route shares the sign-in limit, ten a minute for each source
    // address, so that a stranger cannot guess join tokens. That is invisible to
    // an operator and not to a suite that enrols a dozen hosts from one address
    // in about a minute, whose result then depended on how fast the runner was.
    // Authentication is off here, so there is no sign-in this loosens.
    ZOOMIES_RATE_LIMIT_LOGINS: '1000',
  },
});
