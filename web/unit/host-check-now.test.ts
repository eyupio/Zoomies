import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  CEILING_MS,
  FEATURE,
  KEEP_MS,
  ROLE_SENTENCE,
  WAITING_AFTER_MS,
  checkNow,
  idleTitle,
  superseded,
  unavailableReason,
  type CheckNow,
  type CheckNowInput,
} from '../src/lib/hosts/check-now.ts';

const now = Date.parse('2026-10-08T10:00:00Z');
const at = (offsetMs: number) => new Date(now + offsetMs).toISOString();

function host(over: Partial<CheckNowInput['host']> = {}): CheckNowInput['host'] {
  return { name: 'build-1', healthy: true, features: [FEATURE], ...over };
}
const run = (over: Partial<CheckNowInput> = {}): CheckNow =>
  checkNow({ host: host(), canOperate: true, now, ...over });
const withCheck = (hc: NonNullable<CheckNowInput['host']['health_check']>) =>
  host({ health_check: hc });

// The page used to have no button and a test that asserts there is none matching
// /apply|tune/. A word like "fortunate" would sneak one in through a tooltip, so
// every string this module can produce is held to the same rule.
const FORBIDDEN = /apply|tune/i;

function everyString(): string[] {
  const hosts = [
    host(),
    host({ healthy: false }),
    host({ incompatible: true }),
    host({ incompatible: true, incompatible_reason: 'The agent speaks protocol 3.' }),
    host({ features: [] }),
    withCheck({ state: 'asked', asked_at: at(0) }),
    withCheck({ state: 'asked', asked_at: at(-CEILING_MS) }),
    withCheck({ state: 'failed', asked_at: at(-1000), message: 'It could not run.' }),
    withCheck({ state: 'failed', asked_at: at(-1000) }),
    ...(['first', 'changed', 'unchanged', 'not_newer', 'odd'] as const).map((outcome) =>
      withCheck({
        state: 'done',
        asked_at: at(-1000),
        outcome: outcome as 'first',
        next_at: at(9000),
      }),
    ),
  ];
  const out: string[] = [ROLE_SENTENCE, idleTitle('build-1')];
  for (const h of hosts)
    for (const changes of [0, 1, 3])
      for (const restate of [false, true])
        for (const posting of [false, true])
          for (const localAskedAt of [null, now - 10_000, now - CEILING_MS - 1]) {
            const r = checkNow({
              host: h,
              canOperate: true,
              now,
              changes,
              restate,
              posting,
              localAskedAt,
            });
            out.push(r.title);
            for (const s of [r.reason, r.status, r.announce]) if (s) out.push(s);
          }
  return out;
}

test('no sentence the button can produce mentions applying or tuning', () => {
  const all = everyString();
  assert.ok(all.length > 20);
  for (const s of all) assert.doesNotMatch(s, FORBIDDEN, s);
  // The label itself is the page's, and it must not match either.
  assert.doesNotMatch('Check now', FORBIDDEN);
});

test('a viewer sees no button, only the sentence naming the role', () => {
  // A button the controller would refuse is a trap, and the viewer still needs
  // to know why the page offers nothing.
  const r = run({ canOperate: false });
  assert.equal(r.show, false);
  assert.equal(r.status, ROLE_SENTENCE);
  assert.equal(r.statusKind, 'role');
});

test('an idle host that advertises host-check gets an enabled button and no line', () => {
  const r = run();
  assert.deepEqual(
    { show: r.show, disabled: r.disabled, loading: r.loading, kind: r.statusKind },
    { show: true, disabled: false, loading: false, kind: 'idle' },
  );
  assert.equal(r.status, undefined);
  assert.equal(r.reason, undefined);
  assert.equal(r.title, idleTitle('build-1'));
});

test('the gate is the advertised feature, whatever the report says about containers', () => {
  // The Compose host-health service reports container=false and still cannot
  // answer, so a report flag must never decide this.
  const nativeLooking = {
    ...host({ features: ['tool-cache-fill'] }),
    doctor: { container: false, os: 'linux' },
  };
  const r = run({ host: nativeLooking });
  assert.equal(r.disabled, true);
  assert.match(r.reason ?? '', /cannot check on request/);
  assert.match(r.reason ?? '', /container/);
  const containerLooking = {
    ...host(),
    doctor: { container: true, os: 'linux' },
  };
  assert.equal(run({ host: containerLooking }).disabled, false);
  assert.equal(run({ host: host({ features: undefined }) }).disabled, true);
});

test('each reason the button is off is a sentence beside it, in the controller’s order', () => {
  const cases: [string, CheckNowInput['host'], RegExp][] = [
    ['not connected', host({ healthy: false }), /^build-1 is not connected, so it cannot be asked/],
    [
      'incompatible with the controller’s own words',
      host({ incompatible: true, incompatible_reason: 'The agent speaks protocol 3.' }),
      /^The agent speaks protocol 3\.$/,
    ],
    ['incompatible without words', host({ incompatible: true }), /Update the agent\.$/],
    ['cannot check', host({ features: [] }), /^The agent on build-1 cannot check on request/],
    [
      'not connected wins over the missing flag',
      host({ healthy: false, features: [] }),
      /is not connected/,
    ],
    [
      'incompatible wins over the missing flag',
      host({ incompatible: true, features: [] }),
      /Update the agent/,
    ],
  ];
  for (const [label, h, want] of cases) {
    const r = run({ host: h });
    assert.equal(r.show, true, label);
    assert.equal(r.disabled, true, label);
    assert.match(r.reason ?? '', want, label);
    assert.equal(r.statusKind, 'unavailable', label);
  }
  assert.equal(unavailableReason(host()), null);
});

test('a press in flight reads as asking, and is announced once', () => {
  const r = run({ posting: true });
  assert.equal(r.loading, true);
  assert.equal(r.statusKind, 'asking');
  assert.equal(r.status, 'Asked build-1 to check itself…');
  assert.equal(r.announce, r.status);
});

test('an ask the controller holds is asking, then waiting after five seconds, which is not announced', () => {
  const fresh = run({ host: withCheck({ state: 'asked', asked_at: at(-1000) }) });
  assert.equal(fresh.statusKind, 'asking');
  assert.equal(fresh.loading, true);
  assert.ok(fresh.announce);
  const slow = run({ host: withCheck({ state: 'asked', asked_at: at(-WAITING_AFTER_MS) }) });
  assert.equal(slow.statusKind, 'waiting');
  assert.equal(slow.loading, true);
  assert.match(slow.status ?? '', /^Still waiting for build-1 to answer\./);
  // The sentence changing is not news, so a screen reader is not told again.
  assert.equal(slow.announce, undefined);
});

test('the page’s own press time wins over the server’s clock, which may be skewed', () => {
  const skewed = withCheck({ state: 'asked', asked_at: at(-60_000) });
  assert.equal(run({ host: skewed, localAskedAt: now - 1000 }).statusKind, 'asking');
});

test('an ask the controller still shows after the page’s patience is a failure', () => {
  const r = run({ host: withCheck({ state: 'asked', asked_at: at(-CEILING_MS) }) });
  assert.equal(r.statusKind, 'failed');
  assert.match(r.status ?? '', /^build-1 did not answer in time\./);
  assert.equal(r.loading, false);
  assert.ok(r.failureKey);
  // A failure is inline text and a toast, never a polite announcement read twice.
  assert.equal(r.announce, undefined);
});

test('a controller that forgot the ask leaves the page its own ceiling', () => {
  // Nothing on the view at all: a restart empties the controller's memory.
  const waiting = run({ localAskedAt: now - 10_000 });
  assert.equal(waiting.statusKind, 'waiting');
  assert.equal(waiting.loading, true);
  const failed = run({ localAskedAt: now - CEILING_MS });
  assert.equal(failed.statusKind, 'failed');
  assert.equal(failed.failureKey, `local:${now - CEILING_MS}`);
  const gone = run({ localAskedAt: now - CEILING_MS - KEEP_MS });
  assert.equal(gone.statusKind, 'idle');
  assert.equal(gone.status, undefined);
});

test('a done check says what it found, with the count from the page’s own diff', () => {
  const done = (outcome: string, extra: Partial<CheckNowInput> = {}) =>
    run({
      host: withCheck({
        state: 'done',
        asked_at: at(-3000),
        outcome: outcome as 'first',
      }),
      ...extra,
    });
  assert.equal(done('first').status, 'Checked just now. This is the first report from build-1.');
  assert.equal(
    done('unchanged').status,
    'Checked just now. Nothing has changed since the last report.',
  );
  assert.equal(
    done('changed', { changes: 1 }).status,
    'Checked just now. 1 change since the last report, listed below.',
  );
  assert.equal(
    done('changed', { changes: 4 }).status,
    'Checked just now. 4 changes since the last report, listed below.',
  );
  assert.match(done('changed', { changes: 0 }).status ?? '', /nothing that counts towards/);
  const stale = done('not_newer');
  assert.match(stale.status ?? '', /^build-1 answered, but its clock is behind the last report/);
  for (const o of ['first', 'unchanged', 'changed', 'not_newer'])
    assert.equal(done(o).statusKind, 'done', o);
  // The count may land a beat after the frame, so what is spoken leaves it out.
  assert.equal(
    done('changed', { changes: 3 }).announce,
    'Checked just now. The report has changed.',
  );
  assert.equal(done('unchanged').announce, done('unchanged').status);
});

test('a press inside the cooldown restates the wait and the button stays enabled', () => {
  const hc = {
    state: 'done' as const,
    asked_at: at(-3000),
    outcome: 'unchanged' as const,
    next_at: at(9500),
  };
  const quiet = run({ host: withCheck(hc) });
  assert.equal(quiet.cooling, true);
  assert.equal(quiet.disabled, false);
  assert.equal(quiet.statusKind, 'done');
  const pressed = run({ host: withCheck(hc), restate: true });
  assert.equal(pressed.statusKind, 'cooling');
  assert.equal(pressed.disabled, false);
  assert.equal(pressed.status, 'Checked just now. You can check again in 10 s.');
  // A countdown that ticks is never read out.
  assert.equal(pressed.announce, undefined);
  const later = checkNow({
    host: withCheck(hc),
    canOperate: true,
    now: now + 9000,
    restate: true,
  });
  assert.equal(later.status, 'Checked just now. You can check again in 1 s.');
  const over = checkNow({
    host: withCheck(hc),
    canOperate: true,
    now: now + 9500,
    restate: true,
  });
  assert.equal(over.cooling, false);
  assert.equal(over.statusKind, 'done');
});

test('a failure keeps the cooldown, because failing fast must not make asking free', () => {
  const hc = {
    state: 'failed' as const,
    asked_at: at(-2000),
    message: 'build-1 could not run its checks: boom',
    next_at: at(13_000),
  };
  const r = run({ host: withCheck(hc) });
  assert.equal(r.statusKind, 'failed');
  assert.equal(r.status, hc.message);
  assert.equal(r.cooling, true);
  assert.equal(r.failureKey, `failed:${hc.asked_at}`);
  const pressed = run({ host: withCheck(hc), restate: true });
  assert.equal(pressed.statusKind, 'cooling');
  assert.match(
    pressed.status ?? '',
    /^build-1 was asked a moment ago\. You can ask again in 13 s\.$/,
  );
});

test('a failure with no message from the controller still says something to act on', () => {
  const r = run({ host: withCheck({ state: 'failed', asked_at: at(-2000) }) });
  assert.match(r.status ?? '', /Check that its agent is running/);
});

test('an unavailable host that was just checked keeps the answer and the reason', () => {
  const r = run({
    host: host({
      healthy: false,
      health_check: { state: 'done', asked_at: at(-3000), outcome: 'unchanged' },
    }),
  });
  assert.equal(r.disabled, true);
  assert.match(r.reason ?? '', /not connected/);
  assert.equal(r.statusKind, 'done');
});

test('an answer to the ask that is older than what the page holds is not written over it', () => {
  // The agent can answer before the 202 comes back. Writing the 202 afterwards
  // would put the old report back, and nothing would ever send another frame.
  const asked = { state: 'asked' as const, asked_at: at(-2000) };
  const done = { state: 'done' as const, asked_at: at(-2000), outcome: 'changed' as const };
  assert.equal(superseded(done, asked), true);
  assert.equal(superseded(asked, asked), false);
  assert.equal(superseded(asked, done), false);
  assert.equal(superseded(undefined, asked), false);
  assert.equal(superseded(done, undefined), false);
  // Somebody else asked again since: that ask is newer than this answer.
  assert.equal(superseded({ state: 'asked', asked_at: at(-1000) }, asked), true);
  assert.equal(superseded({ state: 'done', asked_at: at(-5000) }, asked), false);
});
