import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  attemptIsOpen,
  attemptWords,
  buildText,
  confirmControllerUpdate,
  controllerOffer,
  describeMode,
  modeLabel,
  modeSettingHref,
  releaseHref,
  runsRelease,
  soakNote,
  soakText,
  targetLine,
  UPDATE_RESTART,
  updateState,
  type ControllerAttempt,
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
    helper: {
      state: 'missing',
      reason: "No update helper is installed on this controller's host.",
      install_command: 'sudo zoomies updates helper install',
    },
    controller: null,
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

/* -- updating the controller ---------------------------------------------- */

/** An attempt from 1.3.0 to v1.3.2 in the given state, as the status carries it. */
function attempt(state: ControllerAttempt['state'], error = ''): ControllerAttempt {
  return {
    id: 'upd_k3fqz2mx7abcd',
    state,
    from: '1.3.0',
    to: 'v1.3.2',
    trigger: 'manual',
    requested_at: '2026-10-08T11:00:00Z',
    finished_at: state === 'requested' ? null : '2026-10-08T11:05:00Z',
    error,
  };
}

/** A manual status that can be updated: a ready helper and a newer release on offer. */
function updatable(overrides: Partial<UpdatesStatus> = {}): UpdatesStatus {
  return status({
    helper: { state: 'ready', reason: 'The update helper is installed.', install_command: '' },
    ...overrides,
  });
}

test('an attempt in flight is told in the present, and says the page waits for the controller', () => {
  const words = attemptWords(attempt('requested'), '1.3.0');
  assert.equal(words.title, 'Updating to v1.3.2');
  assert.equal(words.label, 'In progress');
  assert.equal(words.open, true);
  assert.match(words.detail, /from 1\.3\.0/);
  assert.match(words.detail, /says how it ended when the controller does, and not before/);
  // Nothing in it may read as having worked.
  assert.doesNotMatch(`${words.title} ${words.detail}`, /updated|succeeded|done|complete/i);
});

test('an attempt the controller closed as succeeded is told as done, with the build it runs', () => {
  const words = attemptWords(attempt('succeeded'), '1.3.2');
  assert.equal(words.title, 'Updated to v1.3.2');
  assert.equal(words.detail, 'This controller was on 1.3.0 and runs 1.3.2 now.');
  assert.equal(words.open, false);
  assert.equal(words.tone, 'accent');
  assert.equal(words.lookAt, '');
});

// The new process records the ending a moment after it starts. In that moment the
// status already carries the answer, in the build it reports; and the converse
// is the rule that matters, that waiting does not make an old build a new one.
test('an open attempt is shown as done only when the controller reports the release it asked for', () => {
  assert.equal(attemptWords(attempt('requested'), '1.3.2').title, 'Updated to v1.3.2');
  assert.equal(attemptWords(attempt('requested'), 'v1.3.2').title, 'Updated to v1.3.2');
  assert.equal(attemptWords(attempt('requested'), '1.3.0').title, 'Updating to v1.3.2');
  assert.equal(attemptWords(attempt('requested'), '1.3.1').title, 'Updating to v1.3.2');
  assert.equal(attemptWords(attempt('requested'), '').title, 'Updating to v1.3.2');
  assert.equal(attemptIsOpen(attempt('requested'), '1.3.0'), true);
  assert.equal(attemptIsOpen(attempt('requested'), '1.3.2'), false);
  assert.equal(attemptIsOpen(null, '1.3.0'), false);
});

test('builds are compared without the v that release tags carry and binaries do not', () => {
  assert.equal(runsRelease('1.3.2', 'v1.3.2'), true);
  assert.equal(runsRelease('v1.3.2', 'v1.3.2'), true);
  assert.equal(runsRelease('1.3.2', 'v1.3.20'), false);
  assert.equal(runsRelease('dev', 'v1.3.2'), false);
  assert.equal(runsRelease('', ''), false);
});

test('an attempt that failed says so, keeps the old build in view and points at the helper', () => {
  const words = attemptWords(attempt('failed', 'checksum mismatch'), '1.3.0');
  assert.equal(words.title, 'The update to v1.3.2 did not succeed');
  assert.equal(words.detail, 'This controller still runs 1.3.0.');
  assert.equal(words.label, 'Failed');
  assert.equal(words.tone, 'danger');
  assert.equal(words.open, false);
  assert.match(words.lookAt, /zoomies updates helper status/);
  assert.match(words.lookAt, /zoomies-update/);
});

// Ninety minutes with no answer is the case where there is nothing else to read,
// so it names where to look.
test('an attempt that timed out says how long it waited and where to look', () => {
  const words = attemptWords(attempt('timed_out'), '1.3.0');
  assert.equal(words.title, 'The update to v1.3.2 timed out');
  assert.equal(
    words.detail,
    'No answer came from the update helper within 90 minutes, and this controller still runs 1.3.0.',
  );
  assert.equal(words.label, 'Timed out');
  assert.equal(words.tone, 'danger');
  assert.equal(words.open, false);
  assert.match(words.lookAt, /zoomies updates helper status/);
  assert.match(words.lookAt, /journalctl -u zoomies-update/);
});

test('an attempt that was cancelled says so without alarm', () => {
  const words = attemptWords(attempt('cancelled'), '1.3.0');
  assert.equal(words.title, 'The update to v1.3.2 was cancelled');
  assert.equal(words.tone, 'neutral');
  assert.equal(words.open, false);
  assert.equal(words.lookAt, '');
});

test('the words for an attempt contain no em dash and no stand-in for one', () => {
  for (const state of ['requested', 'succeeded', 'failed', 'timed_out', 'cancelled'] as const) {
    const words = attemptWords(attempt(state), '1.3.0');
    const all = [words.title, words.detail, words.lookAt, words.label].join('\n');
    assert.doesNotMatch(all, /\u2014| -- /, state);
  }
});

test('the button is offered to the platform role for the release the status names', () => {
  assert.deepEqual(controllerOffer(updatable(), true), { kind: 'offer', tag: 'v1.3.2' });
});

// The reasons are checked in the order an operator would clear them, so that the
// one shown is always the first thing standing in the way.
test('no button is offered below the platform role, and the page says which role it needs', () => {
  const offer = controllerOffer(updatable(), false);
  assert.deepEqual(offer, {
    kind: 'none',
    sentence: 'Updating the controller needs the platform role.',
    installCommand: '',
  });
});

test('no button is offered while updating is off, for a build that is not a release, or with nothing newer', () => {
  const none = (status: UpdatesStatus) => {
    const offer = controllerOffer(status, true);
    assert.equal(offer.kind, 'none');
    return offer.kind === 'none' ? offer.sentence : '';
  };
  assert.match(none(updatable({ mode: 'off' })), /^Updating is off\./);
  assert.match(
    none(updatable({ running: { version: 'dev', release: false } })),
    /not from a release/,
  );
  assert.match(
    none(updatable({ target: { tag: 'v1.3.0', newer: false, due_at: null } })),
    /No newer release is on offer/,
  );
  assert.match(none(updatable({ target: null })), /No newer release is on offer/);
});

test("with no helper the controller's own sentence is shown, with the command that installs it", () => {
  const offer = controllerOffer(status(), true);
  assert.deepEqual(offer, {
    kind: 'none',
    sentence: "No update helper is installed on this controller's host.",
    installCommand: 'sudo zoomies updates helper install',
  });
});

test('while an attempt is open there is no button, and once it ends there is one again', () => {
  assert.deepEqual(controllerOffer(updatable({ controller: attempt('requested') }), true), {
    kind: 'in-flight',
  });
  for (const state of ['failed', 'timed_out', 'cancelled', 'succeeded'] as const) {
    assert.equal(
      controllerOffer(updatable({ controller: attempt(state) }), true).kind,
      'offer',
      state,
    );
  }
});

// The confirmation is the last thing between a click and a restart, so each thing
// it promises is pinned: the release it names, the restart, the jobs, the copy.
test('the confirmation names the release and says what the restart does to jobs and the database', () => {
  const words = confirmControllerUpdate('v1.3.2', '1.3.0');
  assert.equal(words.description, 'Update this controller from 1.3.0 to v1.3.2?');
  assert.equal(words.confirmLabel, 'Update to v1.3.2');
  const lines = words.consequences.join('\n');
  assert.match(lines, /The controller restarts\./);
  assert.match(lines, /Jobs that are running keep running\./);
  assert.match(lines, /If the release changes the database, a copy of it is kept first/);
  assert.match(lines, /A release that changes nothing there takes none\./);
  assert.doesNotMatch(`${words.description}\n${lines}`, /\u2014| -- /);
});

test('the restart state is worded for an update, never for a restore', () => {
  const all = JSON.stringify([
    UPDATE_RESTART.titles,
    UPDATE_RESTART.stopping,
    UPDATE_RESTART.starting,
    UPDATE_RESTART.back,
    UPDATE_RESTART.stuckDown(900),
  ]);
  assert.doesNotMatch(all, /restore|staged|sign-in|fence/i);
  assert.match(UPDATE_RESTART.stuckDown(900), /15 minutes/);
  assert.match(UPDATE_RESTART.stuckDown(900), /zoomies updates helper status/);
  assert.match(UPDATE_RESTART.stuckDown(900), /journalctl -u zoomies-update/);
  assert.doesNotMatch(all, /\u2014| -- /);
});
