import { test } from 'node:test';
import assert from 'node:assert/strict';
import { statusLine, visibleWarnings } from '../src/lib/pools/verdict.ts';
import type { StatusInput } from '../src/lib/pools/verdict.ts';
import type { Result } from '../src/lib/api/types.ts';

type Verdict = Result<'validatePool'>;

const verdict = (over: Partial<Verdict> = {}): Verdict =>
  ({ valid: true, errors: [], warnings: [], matching_hosts: 2, ...over }) as Verdict;

const input = (over: Partial<StatusInput> = {}): StatusInput => ({
  blockers: 0,
  refusals: 0,
  editing: false,
  dirty: true,
  unreachable: false,
  verdict: verdict(),
  fleetKnown: true,
  ...over,
});

// The order of the checks is the whole of this: what the browser can see is
// said before anything the controller thinks, and a controller that has not
// answered is said before it is trusted.
test('a rule being broken is said first, and says what saving would be', () => {
  assert.deepEqual(statusLine(input({ blockers: 1 })), {
    tone: 'danger',
    text: '1 thing to fix before this can be created.',
    action: 'Show',
  });
  assert.equal(
    statusLine(input({ blockers: 3, editing: true })).text,
    '3 things to fix before this can be saved.',
  );
  // Even a verdict that says all is well does not outrank a rule being broken.
  assert.equal(statusLine(input({ blockers: 1, verdict: verdict() })).tone, 'danger');
});

test('an edit that changed nothing says so rather than offering good news', () => {
  assert.deepEqual(statusLine(input({ editing: true, dirty: false })), {
    tone: 'neutral',
    text: 'No changes yet.',
  });
  // A pool being created has always changed.
  assert.notEqual(statusLine(input({ editing: false, dirty: false })).text, 'No changes yet.');
});

test('a controller that did not answer is not reported as agreeing', () => {
  const unreachable = statusLine(input({ unreachable: true, verdict: null }));
  assert.equal(unreachable.tone, 'warning');
  assert.match(unreachable.text, /could not check this pool/);
  assert.deepEqual(statusLine(input({ verdict: null })), {
    tone: 'neutral',
    text: 'Checking with the controller…',
  });
});

test('a refusal the browser did not already say is the controller disagreeing', () => {
  assert.deepEqual(statusLine(input({ refusals: 1 })), {
    tone: 'danger',
    text: 'The controller will not accept this pool yet.',
    action: 'Show',
  });
});

// A pool no host can run is the failure that looks like health, so it is the
// loudest thing the line says once nothing is wrong with the form -- and it
// does not stop the pool being made, because the first pool is made before the
// first agent joins.
test('a pool no host can run says so, and says it is still made', () => {
  const none = statusLine(input({ verdict: verdict({ matching_hosts: 0 }) }));
  assert.equal(none.tone, 'warning');
  assert.match(none.text, /No connected host can run this pool yet/);
  assert.match(none.text, /created anyway/);
  assert.match(
    statusLine(input({ editing: true, verdict: verdict({ matching_hosts: 0 }) })).text,
    /saved anyway/,
  );
  // Before the fleet has been asked a count of none is a blank, not a fact.
  assert.notEqual(
    statusLine(input({ fleetKnown: false, verdict: verdict({ matching_hosts: 0 }) })).tone,
    'warning',
  );
});

test('a pool that can run says how much room it has, in the controller’s own count', () => {
  assert.deepEqual(
    statusLine(input({ verdict: verdict({ matching_hosts: 3, room: { runners: 12 } as never }) })),
    { tone: 'ok', text: 'Room for 12 runners on 3 hosts.' },
  );
  assert.equal(
    statusLine(input({ verdict: verdict({ matching_hosts: 1, room: { runners: 1 } as never }) }))
      .text,
    'Room for 1 runner on 1 host.',
  );
  assert.equal(
    statusLine(input({ verdict: verdict({ matching_hosts: 2 }) })).text,
    '2 connected hosts can run this pool.',
  );
});

test('warnings are counted only when they have nowhere better to be said', () => {
  const rest = {
    code: 'pool.root',
    severity: 'warning',
    title: 'runs as root',
  } as never;
  const room = { code: 'pool.max_above_room', severity: 'warning', title: 'room' } as never;
  const nobody = { code: 'pool.no_matching_hosts', severity: 'warning', title: 'x' } as never;
  const startup = {
    code: 'scheduler.provision_timeout_short',
    severity: 'warning',
    title: 'short',
  } as never;

  const one = verdict({ warnings: [rest, room, nobody, startup] });
  assert.equal(visibleWarnings(one).length, 1);
  assert.equal(statusLine(input({ verdict: one })).text, 'Ready, with a warning to read below.');
  assert.equal(
    statusLine(
      input({
        verdict: verdict({ warnings: [rest, { ...(rest as object), code: 'b' } as never] }),
      }),
    ).text,
    'Ready, with 2 warnings to read below.',
  );
  assert.equal(visibleWarnings(null).length, 0);
});
