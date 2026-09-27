// The first-run fixture: authentication on, an empty database, no external URL.
//
// The monitoring harness (serve.mjs) runs with authentication off and a seeded
// demo fleet, which is exactly right for the monitoring pages and exactly wrong
// for the first-run ones: `session.boot()` short-circuits to `ready` on an
// auth-disabled instance, so the bootstrap card and the sign-in card are
// unreachable, and a seeded fleet is never an unconfigured one. Every defect
// the first-run review found lived in that blind spot.
//
// This is the state a fresh `docker compose up` actually produces.

import { serveController } from './controller.mjs';

serveController({
  port: process.argv[2] ?? '8098',
  prefix: 'zoomies-firstrun-',
  // Authentication stays ON: it is the whole point of this fixture.
  // MCP sign-in is on, although this is plain HTTP on loopback, because
  // mcp-oauth.spec.ts drives the consent screen here: it is the one fixture
  // with authentication on and a person to sign in.
  env: { ZOOMIES_MCP_OAUTH: 'true' },
  // Where to leave the setup token for the spec. playwright.config.ts passes
  // it, from the one definition in tests/support/fixtures.ts.
  tokenFile: process.argv[3],
});
