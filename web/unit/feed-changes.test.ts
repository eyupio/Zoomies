import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { Host, Job, Machine, Problem, Provider } from '../src/lib/api/types.ts';
import {
  cpuChange,
  hostChanges,
  hostSignal,
  installationChange,
  jobFailure,
  jobNews,
  machineChange,
  newProblems,
  providerChanges,
  providerSignal,
  runnerMilestone,
} from '../src/lib/feed/changes.ts';

function host(fields: Partial<Host> = {}): Host {
  return { id: 'hst_1', name: 'builder-1', healthy: true, ...fields } as Host;
}

test('a host that has only moved its CPU reading is not news', () => {
  // The case the whole module exists for: every host publishes a frame on
  // every heartbeat, and a feed that reported them would be a feed nobody
  // reads -- and then one an operator switches off, losing the lines that
  // mattered.
  const before = hostSignal(host({ cpu_percent: 4 } as Partial<Host>));
  const after = hostSignal(host({ cpu_percent: 91 } as Partial<Host>));
  assert.deepEqual(hostChanges(after, before), []);
});

test('a host is reported when it stops answering and again when it comes back', () => {
  const healthy = hostSignal(host());
  const silent = hostSignal(host({ healthy: false }));
  assert.deepEqual(hostChanges(silent, healthy), ['unreachable']);
  assert.deepEqual(hostChanges(healthy, silent), ['recovered']);
});

test('every rung of the throttle ladder is its own line, and calm is one line', () => {
  // A host climbing the ladder is a host under worsening pressure, which is
  // the thing an operator watches for; coming off it happens once.
  const none = hostSignal(host());
  const first = hostSignal(host({ throttle: { level: 1 } } as Partial<Host>));
  const second = hostSignal(host({ throttle: { level: 2 } } as Partial<Host>));
  assert.deepEqual(hostChanges(first, none), ['throttled']);
  assert.deepEqual(hostChanges(second, first), ['throttled']);
  assert.deepEqual(hostChanges(first, second), [], 'stepping back down is not news on its own');
  assert.deepEqual(hostChanges(none, first), ['calm']);
});

test('a host holding new runners under pressure says so once, and says when it stops', () => {
  // Not the same thing as a throttle: a throttle takes slots away for minutes,
  // a hold is this moment's pressure refusing the next start. Both answer
  // "why is nothing starting here".
  const easy = hostSignal(host());
  const pressed = hostSignal(host({ admission_reason: 'load average 19.2 on 8 CPUs' }));
  assert.deepEqual(hostChanges(pressed, easy), ['holding']);
  assert.deepEqual(hostChanges(easy, pressed), ['admitting']);
  // The reason's wording moves with the load; only the transition is news.
  const worse = hostSignal(host({ admission_reason: 'load average 24.7 on 8 CPUs' }));
  assert.deepEqual(hostChanges(worse, pressed), []);
});

test('a runner reaching idle for the first time is the success worth reporting', () => {
  // The half of the fleet that goes right. A panel that only ever reported
  // failures would teach an operator that silence is the good state, which is
  // the same thing as teaching them not to read it.
  assert.equal(runnerMilestone('idle', 'registering'), 'ready');
  assert.equal(runnerMilestone('idle', undefined), 'ready');
  assert.equal(runnerMilestone('removed', 'draining'), 'retired');
});

test('a runner coming back from a job is not ready news again', () => {
  // The job's own line already said that, and a persistent runner would
  // otherwise write one of these after every job it takes.
  assert.equal(runnerMilestone('idle', 'busy'), null);
});

test('the states on the way up are not milestones', () => {
  // Six states per ephemeral runner, one runner per job: a feed that carried
  // them all would be a log.
  assert.equal(runnerMilestone('registering', 'provisioning'), null);
  assert.equal(runnerMilestone('busy', 'idle'), null);
  assert.equal(runnerMilestone('draining', 'busy'), null);
  assert.equal(runnerMilestone('idle', 'idle'), null);
});

test('a runner is reported when its elastic CPU state changes, not when the factor moves', () => {
  // The six states are what the runner's own page shows; the factor behind
  // them moves with every heartbeat.
  assert.equal(cpuChange('zoomies', 'guaranteed'), 'zoomies');
  assert.equal(cpuChange('maximum_zoomies', 'zoomies'), 'maximum_zoomies');
  assert.equal(cpuChange('throttled', 'maximum_zoomies'), 'throttled');
  assert.equal(cpuChange('zoomies', 'zoomies'), null);
  // The lead coming off a runner whose pool lends nothing is news too: it was
  // slowed by its host, and it is back at its share.
  assert.equal(cpuChange('sit_and_stay', 'throttled'), 'sit_and_stay');
  assert.equal(cpuChange('sit_and_stay', 'sit_and_stay'), null);
});

test('a runner already lent CPU when this tab opened is not news', () => {
  // The state it is already in is not something that just happened, and a
  // reconnect delivers one frame per runner.
  assert.equal(cpuChange('maximum_zoomies', undefined), null);
});

test('a host nobody here has seen before has joined the fleet', () => {
  assert.deepEqual(hostChanges(hostSignal(host()), undefined), ['joined']);
});

test('two things going wrong on one host are two lines, worst first', () => {
  const before = hostSignal(host());
  const after = hostSignal(host({ healthy: false, cordoned: true }));
  assert.deepEqual(hostChanges(after, before), ['unreachable', 'cordoned']);
});

/**
 * A host as the controller sends it once its agent has reported: the report,
 * and beside it the controller's count of it. Only the count is read.
 */
function reporting(
  count: { warnings?: number; errors?: number },
  doctor: { container?: boolean; reboot_pending?: boolean; checked_at?: string } = {},
): Partial<Host> {
  return {
    doctor: {
      checked_at: '2026-10-04T10:00:00Z',
      os: 'linux',
      distro: 'ubuntu 24.04',
      container: false,
      reboot_pending: false,
      results: [],
      summary: { counted: 12, warnings: 0, errors: 0, skipped: 0, suggestions: 0, ...count },
      ...doctor,
    },
  } as Partial<Host>;
}

// The report arrives about once a minute per host and its checked_at is
// different every time, so a feed that noticed the report would write a line a
// minute per host. The signal holds the verdict and nothing else.
test('a refreshed report with the same verdict is not news', () => {
  const before = hostSignal(
    host(reporting({ warnings: 2 }, { checked_at: '2026-10-04T10:00:00Z' })),
  );
  const after = hostSignal(
    host(reporting({ warnings: 2 }, { checked_at: '2026-10-04T10:01:00Z' })),
  );
  assert.deepEqual(after, before);
  assert.deepEqual(hostChanges(after, before), []);
});

// Otherwise a rolling upgrade would print "needs attention" once per host, the
// first time each reported, about a state it had been in for months.
test('the first report a host ever sends is a baseline and says nothing', () => {
  const never = hostSignal(host());
  assert.equal(never.reported, false);
  assert.deepEqual(hostChanges(hostSignal(host(reporting({ warnings: 3 }))), never), []);
  assert.deepEqual(hostChanges(hostSignal(host(reporting({ errors: 1 }))), never), []);
  assert.deepEqual(
    hostChanges(hostSignal(host(reporting({}, { reboot_pending: true }))), never),
    [],
    'a reboot already pending on the first report is part of the baseline too',
  );
});

test('a host that starts needing attention says so once, and a worse verdict says it again', () => {
  const calm = hostSignal(host(reporting({})));
  const warned = hostSignal(host(reporting({ warnings: 1 })));
  const failing = hostSignal(host(reporting({ errors: 1, warnings: 1 })));
  assert.deepEqual(hostChanges(warned, calm), ['attention']);
  assert.deepEqual(hostChanges(failing, warned), ['failing']);
  assert.deepEqual(hostChanges(failing, calm), ['failing'], 'straight to an error is an error');
});

test('more warnings, or fewer, are not a new verdict', () => {
  // The count moves as an operator fixes things one at a time, and the host
  // needed attention before and after: that is the same news.
  const one = hostSignal(host(reporting({ warnings: 1 })));
  const three = hostSignal(host(reporting({ warnings: 3 })));
  assert.deepEqual(hostChanges(three, one), []);
  assert.deepEqual(hostChanges(one, three), []);
});

test('an error easing to a warning is not clear, and only reaching zero is', () => {
  const failing = hostSignal(host(reporting({ errors: 1, warnings: 2 })));
  const warned = hostSignal(host(reporting({ warnings: 2 })));
  const calm = hostSignal(host(reporting({})));
  assert.deepEqual(hostChanges(warned, failing), []);
  assert.deepEqual(hostChanges(calm, warned), ['clear']);
  assert.deepEqual(hostChanges(calm, failing), ['clear']);
});

test('a pending reboot is announced when it appears, not while it stays', () => {
  const calm = hostSignal(host(reporting({})));
  const waiting = hostSignal(host(reporting({}, { reboot_pending: true })));
  assert.deepEqual(hostChanges(waiting, calm), ['reboot']);
  assert.deepEqual(hostChanges(waiting, waiting), []);
  assert.deepEqual(hostChanges(calm, waiting), [], 'having rebooted is not a line of its own');
});

test('a verdict and a reboot arriving together are two lines, the verdict first', () => {
  const calm = hostSignal(host(reporting({})));
  const both = hostSignal(host(reporting({ warnings: 1 }, { reboot_pending: true })));
  assert.deepEqual(hostChanges(both, calm), ['attention', 'reboot']);
});

// A container's report skips most checks and warns about its image's
// distribution for ever; the controller raises nothing for it, so the feed
// treats it as no report at all rather than as a host that got better or worse.
test('a container’s partial report is not a verdict', () => {
  const partial = hostSignal(
    host(reporting({ errors: 2, warnings: 4 }, { container: true, reboot_pending: true })),
  );
  assert.equal(partial.reported, false);
  assert.equal(partial.attention, 0);
  assert.equal(partial.rebootPending, false);
});

test('swapping between a full report and a partial one says nothing either way', () => {
  const full = hostSignal(host(reporting({ errors: 1 })));
  const partial = hostSignal(host(reporting({}, { container: true })));
  // Not "clear": nothing got better, the host is simply being looked at
  // through a smaller window.
  assert.deepEqual(hostChanges(partial, full), []);
  // And not a failure when the full report returns, for the same reason it
  // would not be for a host's first.
  assert.deepEqual(hostChanges(full, partial), []);
});

test('a host that goes unreachable says nothing about its health', () => {
  const warned = hostSignal(host(reporting({ warnings: 2 })));
  const silent = hostSignal(host({ ...reporting({ warnings: 2 }), healthy: false }));
  assert.deepEqual(hostChanges(silent, warned), ['unreachable']);
  assert.deepEqual(hostChanges(warned, silent), ['recovered']);
});

test('a host’s reachability comes before what its report says', () => {
  const calm = hostSignal(host(reporting({})));
  const worse = hostSignal(host({ ...reporting({ errors: 1 }), healthy: false }));
  assert.deepEqual(hostChanges(worse, calm), ['unreachable', 'failing']);
});

// A payload with no count is a controller older than the page, or a host that
// has not reported. Reading it must not throw -- this runs on every host frame
// -- and it must not read as a host that is well, or the first count to arrive
// would look like a recovery.
test('a host with no count reads as not reported, and never throws', () => {
  const bare = { checked_at: '2026-10-04T10:00:00Z', container: false, reboot_pending: true };
  for (const fields of [
    {},
    { doctor: undefined },
    { doctor: null },
    { doctor: bare },
    { doctor: { ...bare, summary: undefined } },
    { doctor: { ...bare, summary: null } },
  ]) {
    const signal = hostSignal(host(fields as Partial<Host>));
    assert.equal(signal.reported, false, JSON.stringify(fields));
    assert.equal(signal.attention, 0, JSON.stringify(fields));
    assert.equal(signal.rebootPending, false, JSON.stringify(fields));
  }
  const withCount = hostSignal(host(reporting({ errors: 1 })));
  assert.deepEqual(hostChanges(withCount, hostSignal(host({ doctor: bare } as Partial<Host>))), []);
});

function machine(state: string): Machine {
  return { id: 'mach_1', state, name: 'zoomies-mach-1' } as unknown as Machine;
}

test('a machine is reported when it starts costing money, earns it, or goes wrong', () => {
  assert.equal(machineChange(machine('creating'), undefined), 'creating');
  assert.equal(machineChange(machine('ready'), 'enrolling'), 'ready');
  assert.equal(machineChange(machine('failed'), 'bootstrapping'), 'failed');
  assert.equal(machineChange(machine('quarantined'), 'ready'), 'quarantined');
});

test('the steps on the way up are progress rather than news', () => {
  // The machine's own page has them, with their timings. A feed that carried
  // five lines per machine would bury everything else on a fleet renting any
  // number of them.
  assert.equal(machineChange(machine('starting'), 'creating'), null);
  assert.equal(machineChange(machine('bootstrapping'), 'starting'), null);
  assert.equal(machineChange(machine('enrolling'), 'bootstrapping'), null);
});

test('a machine republished in the state it is already in says nothing', () => {
  assert.equal(machineChange(machine('ready'), 'ready'), null);
});

function job(fields: Partial<Job>): Job {
  return { id: 'job_1', state: 'completed', ...fields } as Job;
}

test('a finished job lands in the category its ending belongs to', () => {
  // The failures are what an operator acts on, so they are their own
  // category and stay on; everything else is the panel this feed replaced,
  // which on a busy fleet is the noisiest thing here.
  assert.equal(jobNews(job({ conclusion: 'failure' })), 'failed');
  assert.equal(jobNews(job({ conclusion: 'timed_out' })), 'failed');
  assert.equal(jobNews(job({ conclusion: 'success' })), 'succeeded');
  assert.equal(jobNews(job({ conclusion: 'cancelled' })), 'cancelled');
  assert.equal(jobFailure('failed'), true);
  assert.equal(jobFailure('runner_lost'), true);
  assert.equal(jobFailure('succeeded'), false);
});

test('a job that has not finished is not news yet', () => {
  // It is on the Overview twice already -- in the running list and in the
  // matrix -- and a feed that repeated every step of it would say nothing.
  assert.equal(jobNews(job({ state: 'in_progress' })), null);
  assert.equal(jobNews(job({ state: 'queued' })), null);
});

test('a job whose runner stopped under it is the fleet’s failure, and says so', () => {
  // GitHub records it as an ordinary failure. The distinction is the one an
  // operator on this page is paid to make, so it survives into the feed --
  // and it survives a conclusion that says the job succeeded, which is what a
  // runner dying between the last step and the report looks like.
  assert.equal(
    jobNews(job({ conclusion: 'failure', runner_fault: 'runner_lost' } as Partial<Job>)),
    'runner_lost',
  );
  assert.equal(
    jobNews(job({ conclusion: 'success', runner_fault: 'runner_lost' } as Partial<Job>)),
    'runner_lost',
  );
});

function provider(fields: Partial<Provider> = {}): Provider {
  return { id: 'prv_1', name: 'proxmox-lab', enabled: true, paused: false, ...fields } as Provider;
}

test('a provider is reported when somebody stops it and when it stops answering', () => {
  const running = providerSignal(provider());
  const paused = providerSignal(provider({ paused: true }));
  const failing = providerSignal(provider({ last_check_error: 'certificate expired' }));
  assert.deepEqual(providerChanges(paused, running), ['paused']);
  assert.deepEqual(providerChanges(running, paused), ['resumed']);
  assert.deepEqual(providerChanges(failing, running), ['unreachable']);
  assert.deepEqual(providerChanges(running, failing), ['reachable']);
});

test('a provider seen for the first time is not an incident', () => {
  // Somebody has just added it, which the audit log records. Reporting it as
  // a change would mean every reconnect announced the whole fleet.
  assert.deepEqual(providerChanges(providerSignal(provider()), undefined), []);
});

test('an installation already failing when the tab opened is still worth saying once', () => {
  assert.equal(installationChange({ id: 'ins_1', healthy: false }, undefined), 'failing');
  assert.equal(installationChange({ id: 'ins_1', healthy: true }, undefined), null);
  assert.equal(installationChange({ id: 'ins_1', healthy: true }, false), 'working');
  assert.equal(installationChange({ id: 'ins_1', healthy: true }, true), null);
});

function problem(code: string, title: string): Problem {
  return { code, title, severity: 'warning' } as Problem;
}

test('a problem is reported once, however many times it is republished', () => {
  const seen = new Set<string>();
  const first = [problem('host.unhealthy', '1 host is not answering')];
  assert.equal(newProblems(first, seen).length, 1);
  assert.equal(newProblems(first, seen).length, 0);
});

test('a problem whose prose has changed is the same problem', () => {
  // "5 webhook deliveries were rejected" becoming "6 webhook deliveries were
  // rejected" is one fault, and re-reporting it every minute is the nagging
  // this identity exists to stop.
  const seen = new Set<string>();
  newProblems([problem('webhook.rejected', '5 deliveries were rejected')], seen);
  assert.equal(
    newProblems([problem('webhook.rejected', '6 deliveries were rejected')], seen).length,
    0,
  );
});

test('a problem that cleared and came back is news again', () => {
  const seen = new Set<string>();
  const fault = [problem('host.unhealthy', '1 host is not answering')];
  newProblems(fault, seen);
  newProblems([], seen);
  assert.equal(newProblems(fault, seen).length, 1);
});
