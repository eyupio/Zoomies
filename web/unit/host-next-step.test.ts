import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { DoctorReport, DoctorResult } from '../src/lib/hosts/health.ts';
import {
  DOCTOR_COMMAND,
  nextStep,
  rebootAdvice,
  rebootPending,
  type NextStep,
  type NextStepInput,
} from '../src/lib/hosts/next-step.ts';

const now = Date.parse('2026-10-04T10:00:00Z');

function result(
  id: string,
  status: DoctorResult['status'],
  tier: DoctorResult['tier'] = 'safe',
): DoctorResult {
  return {
    id,
    title: id,
    tier,
    status,
    current: '',
    recommended: '',
    rationale: '',
    actionable: false,
  };
}

/** A full report: only what the page reads from it is filled in, and `summary` is left out because nextStep never reads it. */
function report(results: DoctorResult[], { reboot = false, container = false } = {}): DoctorReport {
  return {
    checked_at: new Date(now).toISOString(),
    os: 'linux',
    distro: 'ubuntu 24.04',
    container,
    reboot_pending: reboot,
    results,
  };
}

const clean = report([result('disk.space', 'ok')]);
const warned = report([result('disk.space', 'warn')]);
const rebootOnly = report([result('disk.space', 'ok')], { reboot: true });

function input(
  over: Partial<NextStepInput> & { host?: Partial<NextStepInput['host']> } = {},
): NextStepInput {
  return {
    report: rebootOnly,
    cordoned: false,
    ...over,
    host: {
      id: 'host_1',
      name: 'builder-1',
      healthy: true,
      active_runners: 0,
      ...over.host,
    },
  };
}

function step(over: Parameters<typeof input>[0] = {}): NextStep {
  const out = nextStep(input(over));
  assert.ok(out, 'a next step was expected');
  return out;
}

test('there is no next step for a clean host that takes work', () => {
  // Nothing to do and nothing held back: a panel here would be noise on every
  // healthy host's page.
  assert.equal(nextStep(input({ report: undefined })), null);
  assert.equal(nextStep(input({ report: clean })), null);
});

test('a container report gives no next step, even with a reboot flag', () => {
  // The pill ignores a container's reboot flag and the controller raises
  // nothing for it, so a panel that said "reboot" would contradict both.
  const partial = report([result('environment', 'warn')], { reboot: true, container: true });
  assert.equal(rebootPending(partial), false);
  assert.equal(nextStep(input({ report: partial })), null);
});

test('a counted finding or a pending reboot is a next step', () => {
  assert.ok(nextStep(input({ report: warned })));
  assert.ok(nextStep(input({ report: rebootOnly })));
});

test('a kernel.pending warning beside the reboot flag counts once, as the reboot', () => {
  // The agent says a pending reboot twice; the controller counts it once.
  const twice = report([result('kernel.pending', 'warn')], { reboot: true });
  assert.ok(nextStep(input({ report: twice })));
  // And without the flag it is an ordinary counted warning.
  assert.ok(nextStep(input({ report: report([result('kernel.pending', 'warn')]) })));
  assert.equal(nextStep(input({ report: report([result('kernel.pending', 'ok')]) })), null);
});

test('an unreachable host or an unknown count is never called safe or idle', () => {
  // `?? 0` would call a missing count an empty host, and a host nobody can hear
  // has only the last count anyone recorded.
  for (const cordoned of [false, true]) {
    const missing = step({ cordoned, host: { active_runners: undefined } });
    assert.equal(missing.verdict, 'unknown');
    assert.equal(missing.headline, 'Check before rebooting');
    assert.match(missing.detail, /has not said how many runners/);
    assert.equal(missing.runners, 'Not reported');

    for (const healthy of [false, undefined]) {
      const quiet = step({ cordoned, host: { healthy, active_runners: 0 } });
      assert.equal(quiet.verdict, 'unknown');
      assert.match(quiet.detail, /is not connected/);
    }
  }
});

test('runners still on the host are said to be unknown jobs, never counted as jobs', () => {
  const busy = step({ host: { active_runners: 2 } });
  assert.equal(busy.verdict, 'busy');
  assert.equal(busy.headline, 'Runners are still on builder-1');
  assert.equal(busy.runners, '2 runners');
  assert.match(busy.detail, /never finishes by itself/);
  assert.match(busy.detail, /cordon it to stop them/);
  // The count is runners. Nothing here may say how many jobs there are.
  assert.doesNotMatch(busy.runners, /job/);
  assert.match(step({ host: { active_runners: 1 } }).runners, /^1 runner$/);

  const held = step({ cordoned: true, host: { active_runners: 2 } });
  assert.equal(held.verdict, 'busy');
  assert.match(held.detail, /It is cordoned, so no new runner is placed here/);
  assert.match(held.detail, /Reboot builder-1 when none is, then uncordon it/);

  // Findings only: there is nothing to reboot, so the sentence does not say to.
  assert.doesNotMatch(step({ report: warned, host: { active_runners: 2 } }).detail, /Reboot/);
});

test('an idle and cordoned host is safe to reboot, and says what to do afterwards', () => {
  const safe = step({ cordoned: true });
  assert.equal(safe.verdict, 'safe');
  assert.equal(safe.headline, 'Idle and cordoned: safe to reboot now');
  assert.equal(safe.runners, 'None');
  assert.equal(safe.placement, 'Cordoned: no new runner is placed here');
  assert.match(safe.detail, /Reboot builder-1 yourself, then uncordon it/);

  // Findings only: "safe to reboot" is true but nothing asks for a reboot.
  const findingsOnly = step({ cordoned: true, report: warned });
  assert.equal(findingsOnly.verdict, 'safe');
  assert.doesNotMatch(findingsOnly.detail, /Reboot builder-1 yourself/);
  assert.match(findingsOnly.detail, /uncordon it afterwards/);
});

test('an idle host that still takes work is not safe until the cordon is confirmed', () => {
  // This is the in-flight case: the page passes the state from before the
  // click, so a request nobody has heard back from never reads as safe.
  const idle = step({ cordoned: false });
  assert.equal(idle.verdict, 'idle');
  assert.equal(idle.headline, 'Nothing is running on builder-1 right now');
  assert.equal(idle.placement, 'Taking new work');
  assert.match(idle.detail, /Cordon it first so that a job cannot land on it/);
  assert.doesNotMatch(idle.headline, /safe/);

  // Findings only: the reboot sentence is not offered.
  assert.doesNotMatch(step({ report: warned }).detail, /rebooted now/);
});

test('a cordoned host with nothing waiting on it is told to uncordon, and is given no command', () => {
  // The host that was cordoned for a reboot, came back clean and is still
  // cordoned takes no work and nothing on its page would say so. Whatever the
  // report is, the verdict gives no reboot advice, so it holds even for a host
  // that has gone quiet or sent a container's partial report.
  const container = report([result('environment', 'warn')], { reboot: true, container: true });
  for (const r of [clean, undefined, container]) {
    const cordoned = step({ cordoned: true, report: r });
    assert.equal(cordoned.verdict, 'cordoned');
    assert.equal(cordoned.headline, 'builder-1 is cordoned and nothing is waiting on it');
    assert.equal(cordoned.detail, 'Uncordon it so that it takes work again.');
    assert.equal(cordoned.where, null, 'there is nothing to run, so no command is offered');
    assert.equal(cordoned.button.label, 'Uncordon this host');
    assert.match(cordoned.button.help, /lets the scheduler place runners on builder-1 again/);
    assert.equal(cordoned.runners, 'None');
    assert.equal(cordoned.placement, 'Cordoned: no new runner is placed here');
  }
  for (const host of [{ healthy: false }, { healthy: undefined }, { active_runners: undefined }]) {
    assert.equal(step({ cordoned: true, report: clean, host }).verdict, 'cordoned');
  }
  assert.equal(
    step({ cordoned: true, report: clean, host: { active_runners: 3 } }).runners,
    '3 runners',
  );

  // The same clean host, taking work, has no panel at all.
  assert.equal(nextStep(input({ report: clean, cordoned: false })), null);
  // And the moment something is waiting, the verdicts above take over.
  assert.equal(step({ cordoned: true, report: warned }).verdict, 'safe');
  assert.equal(step({ cordoned: true, report: rebootOnly }).verdict, 'safe');
  assert.ok(step({ cordoned: true, report: rebootOnly }).where);
});

test('the command is named for the host and address, and never contains them', () => {
  const caption = step({ host: { address: '10.0.0.7' } }).where;
  assert.equal(caption, 'Run this on builder-1 (10.0.0.7), in a shell on that machine:');
  assert.equal(
    step().where,
    'Run this on builder-1, in a shell on that machine:',
    'no address, no brackets',
  );
  assert.equal(
    step({ host: { embedded: true } }).where,
    "Run this on the controller's own machine (builder-1), in a shell there:",
  );
  assert.match(step({ host: { embedded: true } }).embeddedNote ?? '', /takes Zoomies offline/);
  assert.equal(step().embeddedNote, null);

  assert.equal(DOCTOR_COMMAND, 'sudo zoomies doctor --interactive');
  // A host writes its own name; a name with a newline must not reach the clipboard.
  assert.ok(!DOCTOR_COMMAND.includes('builder'));
});

test('the button says what a press will do', () => {
  const taking = step({ cordoned: false });
  assert.equal(taking.cordoned, false);
  assert.equal(taking.button.label, 'Cordon this host');
  assert.match(taking.button.help, /does not stop or reboot anything/);
  const held = step({ cordoned: true });
  assert.equal(held.cordoned, true);
  assert.equal(held.button.label, 'Uncordon this host');
});

test('a host with no name is called by its id, and then by nothing at all', () => {
  assert.match(step({ host: { name: '' } }).headline, /host_1/);
  assert.match(step({ host: { name: '', id: '' } }).headline, /this host/);
});

test('no sentence on the host page says drain', () => {
  // Drain cordons and then stops a runner that is still busy after five
  // minutes, which is the wrong advice for waiting for jobs to finish. The
  // controller's own fix text for a pending reboot (host_health_problems.go)
  // makes the same choice, and the two should read alike.
  const everything: NextStep[] = [];
  for (const cordoned of [false, true]) {
    for (const r of [clean, warned, rebootOnly, undefined]) {
      for (const host of [
        { active_runners: 0 },
        { active_runners: 2 },
        { active_runners: undefined },
        { healthy: false },
        { embedded: true, address: '10.0.0.7' },
      ]) {
        const out = nextStep(input({ cordoned, report: r, host }));
        if (out) everything.push(out);
      }
    }
  }
  assert.ok(everything.length > 10);
  for (const out of everything) {
    const words = [
      out.headline,
      out.detail,
      out.runners,
      out.placement,
      out.button.label,
      out.button.help,
      out.where ?? '',
      out.embeddedNote ?? '',
    ].join('\n');
    assert.doesNotMatch(words, /drain/i);
  }
  assert.doesNotMatch(rebootAdvice(), /drain/i);
});

test('a viewer is told to cordon, and which role it takes', () => {
  assert.match(rebootAdvice(), /operator role/);
  assert.match(rebootAdvice(), /Zoomies never reboots a host itself/);
});
