import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  byHandLink,
  confirmHostUpdate,
  hostUpdateWords,
  skewFact,
  targetTag,
  type HostUpdateBlock,
} from '../src/lib/hosts/update.ts';

/** A remote host on 1.2.0 under a controller on 1.3.0, whose agent offers to update itself. */
function host(over: Record<string, unknown> = {}, update: Partial<HostUpdateBlock> = {}) {
  return {
    id: 'hst_1',
    name: 'builder-1',
    version: '1.2.0',
    version_skew: 'behind' as const,
    upgrade_version: '1.3.0',
    upgrade_command: "sudo zoomies upgrade --mode agent --version 'v1.3.0'",
    os: 'linux',
    embedded: false,
    update: {
      state: 'none' as const,
      reason: 'builder-1 runs 1.2.0 and can be updated to v1.3.0.',
      can_update: true,
      attempt_id: '',
      ...update,
    },
    ...over,
  };
}

test('a host that is behind and can be updated is offered the button, and its state is not a status colour', () => {
  const words = hostUpdateWords(host());
  assert.ok(words);
  assert.equal(words.canPress, true);
  assert.equal(words.action, 'Update');
  assert.equal(words.inFlight, false);
  // Behind is a fact about the release and not a state the host is in.
  assert.equal(words.tone, 'neutral');
  assert.equal(words.sentence, 'builder-1 runs 1.2.0 and can be updated to v1.3.0.');
});

test('behind, ahead and different are neutral facts and a matching host has none', () => {
  for (const skew of ['behind', 'ahead', 'differs'] as const) {
    const fact = skewFact(skew);
    assert.ok(fact, skew);
    assert.equal(fact.tone, 'neutral', skew);
  }
  assert.equal(skewFact('behind')?.label, 'Behind');
  assert.equal(skewFact('ahead')?.label, 'Ahead');
  assert.equal(skewFact('differs')?.label, 'Different build');
  assert.equal(skewFact(undefined), null);
  assert.equal(skewFact(''), null);
});

test('each state of an attempt has a label, a tone and whether it can be pressed', () => {
  const rows = [
    // state, can_update, label, tone, drawn, canPress, inFlight
    ['none', true, 'Can be updated', 'neutral', true, true, false],
    ['none', false, 'Update by command', 'neutral', true, false, false],
    ['requested', false, 'Updating', 'accent', false, false, true],
    ['succeeded', false, 'Updated', 'accent', false, false, false],
    ['failed', true, 'Failed', 'danger', true, true, false],
    ['failed', false, 'Failed', 'danger', true, false, false],
    ['timed_out', true, 'Timed out', 'danger', true, true, false],
    ['cancelled', true, 'Cancelled', 'neutral', true, true, false],
  ] as const;
  for (const [state, can, label, tone, offered, canPress, inFlight] of rows) {
    // An attempt that ended without the update leaves the host behind, and so
    // does a running one; a success is a host that matches the controller.
    const where = state === 'succeeded' ? { version: '1.3.0', version_skew: undefined } : {};
    const words = hostUpdateWords(host(where, { state, can_update: can, reason: 'Why.' }));
    assert.ok(words, `${state}/${can}`);
    assert.equal(words.label, label, `${state}/${can} label`);
    assert.equal(words.tone, tone, `${state}/${can} tone`);
    assert.equal(words.offered, offered, `${state}/${can} offered`);
    assert.equal(words.canPress, canPress, `${state}/${can} canPress`);
    assert.equal(words.inFlight, inFlight, `${state}/${can} inFlight`);
  }
});

test('a failed update can be tried again, and says so in its action', () => {
  const words = hostUpdateWords(host({}, { state: 'failed', reason: 'The helper said no.' }));
  assert.equal(words?.action, 'Try again');
  assert.equal(words?.sentence, 'The helper said no.');
});

test('a request that is open is never shown as done while the host reports its old release', () => {
  const words = hostUpdateWords(
    host(
      {},
      {
        state: 'requested',
        can_update: false,
        attempt_id: 'upd_1',
        reason: 'The update to v1.3.0 has been asked for.',
      },
    ),
  );
  assert.equal(words?.label, 'Updating');
  assert.equal(words?.inFlight, true);
  assert.equal(words?.canPress, false);
  assert.doesNotMatch(words?.live ?? '', /updated|done|now runs/i);
  assert.doesNotMatch(words?.sentence ?? '', /updated\b/i);
});

test('the host reporting the release is the update done, before the controller has closed the attempt', () => {
  // The heartbeat that carries the version and the one that closes the attempt
  // are one, but their frames are not: the version is what the attempt waited for.
  const words = hostUpdateWords(
    host({ version: '1.3.0', version_skew: undefined }, { state: 'requested', can_update: false }),
  );
  assert.equal(words?.label, 'Updated');
  assert.equal(words?.inFlight, false);
  assert.equal(words?.sentence, 'builder-1 runs 1.3.0 now.');
  assert.match(words?.live ?? '', /builder-1: updated/);
});

test('an attempt the controller closed as succeeded is shown as done, in the version the host reports', () => {
  const words = hostUpdateWords(
    host({ version: '1.3.0', version_skew: undefined }, { state: 'succeeded', can_update: false }),
  );
  assert.equal(words?.label, 'Updated');
  assert.equal(words?.sentence, 'builder-1 runs 1.3.0 now.');
});

test('an update that worked for an earlier release does not hide that the host is behind again', () => {
  // The controller has moved on since: the latest attempt is a success, and the
  // host is behind the new release and can be asked again.
  const words = hostUpdateWords(host({}, { state: 'succeeded', can_update: true }));
  assert.equal(words?.label, 'Can be updated');
  assert.equal(words?.canPress, true);
});

test('the controller’s sentence is passed through as text and never interpreted', () => {
  const sentence = '<img src=x onerror=alert(1)> **bold** [a](javascript:alert(1))';
  const words = hostUpdateWords(host({}, { state: 'failed', reason: sentence }));
  assert.equal(words?.sentence, sentence);
});

test('a host that matches the controller, or is ahead of it, has no row unless an attempt is worth showing', () => {
  assert.equal(hostUpdateWords(host({ version_skew: undefined }, { can_update: false })), null);
  assert.equal(hostUpdateWords(host({ version_skew: 'ahead' }, { can_update: false })), null);
  // An attempt that failed and left the host behind is worth showing; one the
  // controller dropped because the host got there another way arrives as none.
  assert.ok(
    hostUpdateWords(host({ version_skew: undefined }, { state: 'succeeded', can_update: false })),
  );
});

test('the agent inside the controller and a controller that sends no block have no row', () => {
  assert.equal(hostUpdateWords(host({ embedded: true })), null);
  assert.equal(hostUpdateWords(host({ update: undefined })), null);
});

test('a host that cannot be updated from here says why in the controller’s words and offers no button', () => {
  const why =
    'This host’s agent does not offer to update itself. Run sudo zoomies updates helper install there, or update it with the command below.';
  const words = hostUpdateWords(host({}, { can_update: false, reason: why }));
  assert.equal(words?.canPress, false);
  assert.equal(words?.sentence, why);
  assert.equal(words?.label, 'Update by command');
});

// With updating off the controller refuses every press, so the card must not
// hold a button that only ever says no: what it holds is the controller's
// sentence saying why, as text, and a button that cannot be pressed.
test('with updating off the card says so as text and nothing on it can be pressed', () => {
  const why =
    'Updating is off, so hosts are not updated from here. Somebody with the platform role can turn it on by setting updates.mode to manual or auto on the Configuration page.';
  const words = hostUpdateWords(host({}, { can_update: false, reason: why }));
  assert.ok(words);
  assert.equal(words.canPress, false);
  assert.equal(words.sentence, why);
  assert.equal(words.inFlight, false);
  // An update that failed before updating was switched off is not offered again.
  const failed = hostUpdateWords(
    host({}, { state: 'failed', can_update: false, reason: 'It failed.' }),
  );
  assert.equal(failed?.canPress, false);
});

// The helper can never be installed there, so a button that can never be
// pressed would only be a promise; the reason is the card's text, and the
// command beneath the card is the way.
test('a host the helper can never be installed on says why, draws no button and suggests no install', () => {
  const why =
    'The update helper cannot be installed on this host: its agent runs on a host that systemd does not run, and the update helper is a pair of systemd units. Update it on the host with the command below.';
  const words = hostUpdateWords(host({}, { state: 'unsupported', can_update: false, reason: why }));
  assert.equal(words?.label, 'Update by command');
  assert.equal(words?.sentence, why);
  assert.equal(words?.offered, false);
  assert.equal(words?.canPress, false);
  assert.equal(words?.tone, 'neutral');
  assert.doesNotMatch(words?.sentence ?? '', /helper install/);
});

// zoomies upgrade knows no Windows service and Windows has no sudo, so the
// controller sends a Windows agent no command. Its card must not promise one:
// it says the update is by hand and links to the steps.
test('a Windows host is updated by hand, and its card links to the steps rather than promising a command', () => {
  const why =
    'The update helper cannot be installed on this host: its agent runs on an operating system other than Linux, and the update helper is a pair of systemd units, which need Linux. Update it by hand on the host, with the steps at the foot of this card.';
  const windows = host(
    { os: 'windows', upgrade_command: undefined, upgrade_note: 'Update this one by hand.' },
    { state: 'unsupported', can_update: false, reason: why },
  );
  assert.equal(hostUpdateWords(windows)?.label, 'Update by hand');
  assert.match(byHandLink(windows), /\/upgrading\/#upgrading-an-agent-host$/);
  // A host that has a command to copy needs no link: the command is the way.
  for (const os of ['linux', 'darwin']) {
    const other = host({ os }, { state: 'unsupported', can_update: false, reason: why });
    assert.equal(hostUpdateWords(other)?.label, 'Update by command', os);
    assert.equal(byHandLink(other), '', os);
  }
});

test('a host the helper cannot be installed on has no row once it runs the controller’s release', () => {
  assert.equal(
    hostUpdateWords(host({ version_skew: '' }, { state: 'unsupported', can_update: false })),
    null,
  );
});

test('the target is named with a v whether the controller reports one or not', () => {
  assert.equal(targetTag({ upgrade_version: '1.3.0' }), 'v1.3.0');
  assert.equal(targetTag({ upgrade_version: 'v1.3.0' }), 'v1.3.0');
  assert.equal(targetTag({ upgrade_version: ' 1.3.0 ' }), 'v1.3.0');
  assert.equal(targetTag({ upgrade_version: '' }), '');
  assert.equal(targetTag({}), '');
});

test('the confirmation names the host and the release, and says what is true of the restart', () => {
  const words = confirmHostUpdate('builder-1', '1.2.0', 'v1.3.0');
  assert.match(words.title, /builder-1/);
  assert.equal(words.description, 'Update the agent on builder-1 from 1.2.0 to v1.3.0?');
  assert.equal(words.confirmLabel, 'Update to v1.3.0');
  const text = words.consequences.join('\n');
  assert.match(text, /The agent on builder-1 restarts\./);
  // Running jobs are not waited for and are not killed: the unit leaves the
  // runners alone and the restarted agent adopts them.
  assert.match(text, /Jobs that are running keep running/);
  assert.match(text, /does not wait for them to finish/);
  assert.match(text, /reports back when it runs v1\.3\.0/);
  assert.match(text, /not before/);
});

test('the confirmation does not claim the restart waits for jobs', () => {
  const text = confirmHostUpdate('builder-1', '1.2.0', 'v1.3.0').consequences.join(' ');
  assert.doesNotMatch(text, /after (they|the jobs?) finish/i);
});

test('the words on a host’s card keep to the repository’s voice', () => {
  const all = [
    ...confirmHostUpdate('builder-1', '1.2.0', 'v1.3.0').consequences,
    confirmHostUpdate('builder-1', '1.2.0', 'v1.3.0').description,
    skewFact('behind')?.hint ?? '',
    hostUpdateWords(host({}, { state: 'failed', reason: 'x' }))?.live ?? '',
  ].join('\n');
  assert.doesNotMatch(all, /—| -- /);
});
