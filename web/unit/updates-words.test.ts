import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  buildText,
  describeMode,
  modeLabel,
  modeSettingHref,
  releaseHref,
  soakNote,
  soakText,
  targetLine,
  updateState,
} from '../src/lib/updates/words.ts';
import { atLeast, type Role, type UpdatesStatus } from '../src/lib/api/types.ts';

/** A moment to count from, so the sentences below do not depend on when the suite runs. */
const NOW = Date.parse('2026-10-08T12:00:00Z');
const HOUR = 3_600_000;
const MINUTE = 60_000;

/** The status of a controller on 1.3.0 in manual mode, with v1.3.2 on offer. */
function status(overrides: Partial<UpdatesStatus> = {}): UpdatesStatus {
  return {
    mode: 'manual',
    soak: '24h',
    running: { version: '1.3.0', release: true },
    latest: {
      tag: 'v1.3.2',
      url: 'https://example.invalid/releases/v1.3.2',
      published_at: '2026-10-08T06:00:00Z',
    },
    target: { tag: 'v1.3.2', newer: true, due_at: null },
    reason:
      'Available: v1.3.2 is newer than the v1.3.0 running now; manual mode waits for someone to update.',
    checked_at: '2026-10-08T11:58:00Z',
    ...overrides,
  };
}

/** An auto status whose release can be taken `ms` after NOW. */
function autoDueIn(ms: number): UpdatesStatus {
  return status({
    mode: 'auto',
    target: { tag: 'v1.3.2', newer: true, due_at: new Date(NOW + ms).toISOString() },
  });
}

// Each mode is told with what it costs, because the choice is made against the
// sentence and nobody reads the settings reference first.
test('each mode says what it would do, and auto says what its wait costs', () => {
  assert.equal(
    describeMode('off'),
    'Zoomies tells you when a newer release exists and does nothing about it. Updating stays a command you run yourself.',
  );
  assert.equal(
    describeMode('manual'),
    'Zoomies would offer the newest release that can be installed on this system and wait for a person to take it. Nothing would move until someone asks.',
  );
  assert.equal(
    describeMode('auto'),
    'Zoomies would take the newest release that can be installed on this system once it has been public for the soak. A newer release restarts the wait, so if releases are published faster than the soak, auto never takes one.',
  );
});

// This part of the product installs nothing, so a sentence in the future tense
// would promise what nothing keeps. A mode that acts is described in the
// conditional until the part that acts has landed.
test('the modes that would act are described in the conditional and never promise', () => {
  for (const mode of ['manual', 'auto'] as const) {
    assert.match(describeMode(mode), /\bwould\b/, mode);
    assert.doesNotMatch(describeMode(mode), /\bwill\b|\binstalling\b/, mode);
  }
});

test('a mode is labelled in the word an operator sets it by', () => {
  assert.equal(modeLabel('off'), 'Off');
  assert.equal(modeLabel('manual'), 'Manual');
  assert.equal(modeLabel('auto'), 'Auto');
});

test('auto names the time it would take a release in whole hours, counted down from now', () => {
  assert.equal(targetLine(autoDueIn(18 * HOUR), NOW), 'Auto would take v1.3.2 in 18 hours.');
  assert.equal(targetLine(autoDueIn(1 * HOUR), NOW), 'Auto would take v1.3.2 in 1 hour.');
});

// The status sentence rounds down ("only ever errs short"), and this line sits
// beside it on the page, so the two have to count the same way or an operator is
// shown two different times for one release.
test('the time left is rounded down, as the sentence beside it is', () => {
  assert.equal(
    targetLine(autoDueIn(17 * HOUR + 59 * MINUTE), NOW),
    'Auto would take v1.3.2 in 17 hours.',
  );
  assert.equal(targetLine(autoDueIn(2 * HOUR - 1), NOW), 'Auto would take v1.3.2 in 1 hour.');
});

test('in the last hour auto counts minutes, and below one it says less than a minute', () => {
  assert.equal(targetLine(autoDueIn(45 * MINUTE), NOW), 'Auto would take v1.3.2 in 45 minutes.');
  // Rounded down here as well: forty seconds into a minute is not the next one.
  assert.equal(
    targetLine(autoDueIn(45 * MINUTE + 40_000), NOW),
    'Auto would take v1.3.2 in 45 minutes.',
  );
  assert.equal(targetLine(autoDueIn(1 * MINUTE), NOW), 'Auto would take v1.3.2 in 1 minute.');
  assert.equal(targetLine(autoDueIn(20_000), NOW), 'Auto would take v1.3.2 in less than a minute.');
});

test('auto says now once the soak is over, and the instant it ends is already over', () => {
  assert.equal(targetLine(autoDueIn(0), NOW), 'Auto would take v1.3.2 now.');
  assert.equal(targetLine(autoDueIn(-3 * HOUR), NOW), 'Auto would take v1.3.2 now.');
});

test('manual has no due time and says who it waits for', () => {
  assert.equal(
    targetLine(status(), NOW),
    'Manual would offer v1.3.2 and wait for a person to take it.',
  );
});

// An auto target always has a due time, but a frame this page cannot read the
// time from must still say what it can rather than a date that is not a date.
test('auto without a due time, or with one that cannot be read, names the release alone', () => {
  const target = { tag: 'v1.3.2', newer: true };
  assert.equal(
    targetLine(status({ mode: 'auto', target: { ...target, due_at: null } }), NOW),
    'Auto would take v1.3.2.',
  );
  assert.equal(
    targetLine(status({ mode: 'auto', target: { ...target, due_at: 'soon' } }), NOW),
    'Auto would take v1.3.2.',
  );
});

// Only a release that is ahead of the build is something to take. The sentence
// that says why there is nothing is the controller's own, shown beside this.
test('there is no line when there is nothing to take', () => {
  const behind = { tag: 'v1.3.2', due_at: null };
  assert.equal(targetLine(status({ target: { ...behind, newer: false } }), NOW), '');
  assert.equal(targetLine(status({ target: null, latest: null }), NOW), '');
  assert.equal(targetLine(status({ mode: 'off', target: null, latest: null }), NOW), '');
});

test('the state is the word the controller opens its own sentence with', () => {
  const cases: [string, UpdatesStatus, string, string][] = [
    [
      'off',
      status({ mode: 'off', latest: null, target: null, checked_at: null }),
      'Off',
      'neutral',
    ],
    [
      'a build from main',
      status({
        running: { version: 'main-sha-abc1234', release: false },
        latest: null,
        target: null,
        checked_at: null,
      }),
      'Left alone',
      'neutral',
    ],
    [
      'a build from a describe that did read the list',
      status({ running: { version: 'dev', release: false }, latest: null, target: null }),
      'Left alone',
      'neutral',
    ],
    [
      'a list nobody has read',
      status({ latest: null, target: null, checked_at: null }),
      'Not read yet',
      'neutral',
    ],
    [
      'a list with nothing to install',
      status({ latest: null, target: null }),
      'Nothing to take',
      'neutral',
    ],
    [
      'a build that is not behind',
      status({ target: { tag: 'v1.3.2', newer: false, due_at: null } }),
      'Nothing newer',
      'neutral',
    ],
    ['manual with a release ahead', status(), 'Available', 'accent'],
    ['auto inside its soak', autoDueIn(18 * HOUR), 'Waiting', 'accent'],
    ['auto past its soak', autoDueIn(-HOUR), 'Ready', 'accent'],
    [
      'auto with a due time that cannot be read',
      status({ mode: 'auto', target: { tag: 'v1.3.2', newer: true, due_at: 'soon' } }),
      'Available',
      'accent',
    ],
  ];
  for (const [name, input, label, tone] of cases) {
    assert.deepEqual(updateState(input, NOW), { label, tone }, name);
  }
});

// The state may only be news, never alarm: nothing is wrong when a release is
// waiting, and the status colours are reserved for the fleet.
test('no state borrows a status colour', () => {
  for (const input of [status(), autoDueIn(HOUR), autoDueIn(-HOUR), status({ mode: 'off' })]) {
    assert.ok(['neutral', 'accent'].includes(updateState(input, NOW).tone));
  }
});

test('the soak is said as a period, and zero as no wait', () => {
  assert.equal(soakText('24h'), '24 hours');
  assert.equal(soakText('48h'), '2 days');
  assert.equal(soakText('90m'), '90 minutes');
  assert.equal(soakText('1h30m'), '90 minutes');
  assert.equal(soakText('0s'), 'No wait');
  assert.equal(soakText('0'), 'No wait');
  // A spelling this page cannot read is shown as it came, not dropped.
  assert.equal(soakText('a day'), 'a day');
});

test('the soak says who it applies to', () => {
  assert.equal(soakNote('auto'), 'Counted from the day GitHub published the release.');
  assert.equal(soakNote('manual'), 'Only auto waits; manual and off ignore it.');
  assert.equal(soakNote('off'), 'Only auto waits; manual and off ignore it.');
});

// The page links to whatever address the release check recorded, and that is
// text GitHub sent. A link is offered only to an address that is plainly a web
// page over TLS; anything else is shown as the tag it names.
test('a release is linked only when its address is an https one', () => {
  assert.equal(
    releaseHref('https://github.com/eyupio/zoomies/releases/tag/v1.3.2'),
    'https://github.com/eyupio/zoomies/releases/tag/v1.3.2',
  );
  for (const unsafe of [
    '',
    ' ',
    'http://example.invalid/releases/v1.3.2',
    'javascript:alert(1)',
    'data:text/html,<script>alert(1)</script>',
    '//example.invalid/releases/v1.3.2',
    '/releases/v1.3.2',
    ' https://example.invalid/releases/v1.3.2',
    'ftp://example.invalid/releases/v1.3.2',
  ]) {
    assert.equal(releaseHref(unsafe), null, JSON.stringify(unsafe));
  }
  assert.equal(releaseHref(undefined), null);
});

test('a build is told apart by whether it came from a release', () => {
  assert.equal(buildText(true), 'From a release');
  assert.equal(buildText(false), 'Not from a release, so updates leave it alone');
});

// The settings list omits platform-scoped rows below that role, so a link sent
// to an administrator opens a Configuration page that cannot show the setting.
test('the link to the mode setting is offered to the platform role and to no one below it', () => {
  const as = (held: Role) => (needed: Role) => atLeast(held, needed);
  assert.equal(modeSettingHref(as('platform')), '/settings/configuration?setting=updates.mode');
  for (const held of ['admin', 'operator', 'viewer'] as const) {
    assert.equal(modeSettingHref(as(held)), null, held);
  }
});
