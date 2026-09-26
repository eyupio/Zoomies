// The status fixture: the diagnostics fleet, with authentication on and the
// status page public.
//
// The status page is for somebody with no account, so the only honest place
// to test it is a controller that has accounts switched on and is not signed
// in to: with authentication off, every request is an administrator and
// "signed out" is not a state the page can be in. The fleet is the same
// deterministic seed as the diagnostics fixture -- a pool nothing can place,
// a held job, two stuck runners -- so the page has reasons to show, and the
// spec reads the fleet's names from that fixture, which answers without a
// sign-in, to search this one's page for them.

import { serveController } from './controller.mjs';

serveController({
  port: process.argv[2] ?? '8095',
  prefix: 'zoomies-e2e-status-',
  env: {
    ZOOMIES_STATUS_MODE: 'public',
    ZOOMIES_SEED_DEMO: 'true',
    ZOOMIES_SEED_STUCK: 'true',
    // As the diagnostics fixture, so the two seeds stay the same fleet for
    // as long as the suite runs.
    ZOOMIES_PROVISION_TIMEOUT: '4h',
  },
});
