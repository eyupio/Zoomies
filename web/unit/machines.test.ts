/**
 * The provider and machine machinery that has no DOM: what a rented machine is
 * doing, and the wizard draft that buys one.
 *
 * `status.ts` imports Lucide icon components, so it cannot be loaded outside a
 * browser as it stands -- which is why `outcomes.ts` exists at all. The hook
 * below swaps `@lucide/svelte` for a module exporting the same names as plain
 * functions, so the state map itself can be tested here: it is the one place
 * where a machine state that nobody mapped would be painted like a healthy
 * machine, and that is not a failure a Playwright pass would catch either.
 *
 * The stubbed names are read out of `status.ts` rather than listed here, so
 * adding an icon to the state map does not break this file.
 */
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks } from 'node:module';
import {
  MACHINE_FLOW,
  formatCost,
  lifecycleCounts,
  machineCost,
  machineSignals,
  machinesNeedingReview,
  phaseLabel,
  phaseState,
} from '../src/lib/providers/machines.ts';
import {
  FIELD_LABELS,
  STEP_FIELDS,
  WIZARD_STEPS,
  applySettingDefaults,
  draftErrors,
  draftFromProvider,
  emptyDraft,
  stepForField,
  toProviderBody,
  willRentNothing,
} from '../src/lib/providers/draft.ts';
import { MACHINE_STATES } from '../src/lib/api/types.ts';
import type { Machine, MachineState, Provider, ProviderSetting } from '../src/lib/api/types.ts';

const LUCIDE = 'zoomies-test:lucide-stub';
const iconNames = (
  /import\s*\{([^}]*)\}\s*from\s*'@lucide\/svelte'/.exec(
    readFileSync(new URL('../src/lib/status.ts', import.meta.url), 'utf8'),
  )?.[1] ?? ''
)
  .split(',')
  .map((name) => name.trim())
  .filter((name) => name !== '');

registerHooks({
  resolve(specifier, context, next) {
    if (specifier === '@lucide/svelte')
      return { url: LUCIDE, format: 'module', shortCircuit: true };
    return next(specifier, context);
  },
  load(url, context, next) {
    if (url === LUCIDE)
      return {
        format: 'module',
        shortCircuit: true,
        source: iconNames.map((name) => `export function ${name}() {}`).join('\n'),
      };
    return next(url, context);
  },
});

const { cpuResourceStatus, machineStatus, toneTokens } = await import('../src/lib/status.ts');

test('elastic CPU states keep their Zoomies vocabulary and distinct brand icons', () => {
  const maximum = cpuResourceStatus('maximum_zoomies');
  const boost = cpuResourceStatus('zoomies');
  const throttled = cpuResourceStatus('throttled');
  const steady = cpuResourceStatus('guaranteed');
  const sitting = cpuResourceStatus('sit_and_stay');

  assert.equal(maximum.label, 'Squirrel spotted, maximum zoomies');
  assert.equal(boost.label, 'Rabbit spotted, extra zoomies');
  assert.equal(throttled.label, 'Leash tightened, host under pressure');
  assert.equal(steady.label, 'Steady paws, guaranteed pace');
  assert.equal(sitting.label, 'Sit and stay, CPU held at its share');
  assert.notEqual(maximum.icon, boost.icon);
  assert.notEqual(boost.icon, throttled.icon);
  assert.equal(maximum.tone, 'busy');
  // Pending, as the status table in docs/ui-guidelines.md has it: throttled is
  // amber for a runner's CPU and for a host alike, and the plum is draining,
  // cordoned and paused.
  assert.equal(throttled.tone, 'pending');
  // A held runner is not an idle one: "guaranteed pace" is the elastic
  // pool's word for a runner that may yet be lent something, and this one
  // never will be. Its own dog, sitting, and no status hue.
  assert.notEqual(sitting.icon, steady.icon);
  assert.equal(sitting.tone, 'neutral');
  assert.equal(sitting.key, 'sit_and_stay');
});

test("with the vocabulary off, the controller's kennel label gives way to a plain one", () => {
  // The controller names every CPU state in the kennel's words, and the feed
  // passes that label through; with the vocabulary off it must not reach the
  // page beside a plain icon.
  for (const [state, controller, plain] of [
    ['maximum_zoomies', 'Squirrel spotted, maximum zoomies', 'Maximum boost'],
    ['zoomies', 'Rabbit spotted, extra zoomies', 'Extra boost'],
    ['throttled', 'Leash tightened, host under pressure', 'Throttled'],
    ['observing', 'Nose to the wind, watching spare CPU', 'Observing spare CPU'],
    ['sit_and_stay', 'Sit and stay, CPU held at its share', 'Held at its share'],
    ['guaranteed', 'Steady paws, guaranteed pace', 'Guaranteed pace'],
  ] as const) {
    assert.equal(cpuResourceStatus(state, controller, false).label, plain);
    assert.equal(cpuResourceStatus(state, controller).label, controller);
  }
});

test('a machine carrying work is not drawn with a Play button', () => {
  assert.equal(machineStatus('ready', true).icon.name, 'Activity');
  assert.notEqual(machineStatus('ready', false).icon.name, machineStatus('ready', true).icon.name);
});

/** The six status hues. Nothing a machine is doing may invent a seventh. */
const TONES = ['idle', 'busy', 'pending', 'draining', 'danger', 'neutral'];

const NOW = Date.parse('2026-09-14T12:00:00Z');

function machine(fields: Partial<Machine> = {}): Machine {
  return {
    id: 'mach_k3f9qz2mx7ab',
    provider_id: 'prv_k3f9qz2mx7ab',
    name: 'zoomies-mach-k3f9q',
    state: 'ready',
    labels: {},
    safe_to_delete: false,
    timeline: [],
    created_at: '2026-09-14T11:00:00Z',
    updated_at: '2026-09-14T11:30:00Z',
    ...fields,
  };
}

function spec(fields: Partial<ProviderSetting> & { key: string }): ProviderSetting {
  return { label: fields.key, kind: 'text', required: false, advanced: false, ...fields };
}

/* -- the state map -------------------------------------------------------- */

test('every machine state has a status of its own, and anything else is neutral rather than healthy', () => {
  // The default arm of the switch is the one that goes wrong: a state added to
  // the API and forgotten here would otherwise be drawn in whatever the last
  // case happened to be, and a quarantined machine painted like a ready one is
  // a resource nobody goes and looks at.
  assert.equal(MACHINE_STATES.length, 11, 'eleven states, as the store enforces');
  const keys = new Set<string>();
  for (const state of MACHINE_STATES) {
    const status = machineStatus(state);
    assert.notEqual(status.key, 'unknown', `${state} falls through to the default`);
    assert.ok(TONES.includes(status.tone), `${state} invents the tone ${status.tone}`);
    assert.ok(status.label !== '', `${state} has no label`);
    assert.ok(status.hint, `${state} has no sentence an operator can act on`);
    keys.add(status.key);
  }
  assert.equal(keys.size, MACHINE_STATES.length, 'two states share one key');

  const unknown = machineStatus(undefined);
  assert.equal(unknown.key, 'unknown');
  assert.equal(unknown.tone, 'neutral');
  assert.equal(machineStatus('nonsense' as MachineState).key, 'unknown');
});

test('a machine is drawn in the tone the rest of the product gives the same idea', () => {
  // An operator learns one vocabulary: a machine being built reads as pending
  // exactly as a runner being provisioned does, and the two ways a machine
  // ends up needing a person are the only ones that read as danger.
  for (const state of ['planned', 'creating', 'starting', 'bootstrapping', 'enrolling'] as const)
    assert.equal(machineStatus(state).tone, 'pending', state);
  for (const state of ['draining', 'deleting'] as const)
    assert.equal(machineStatus(state).tone, 'draining', state);
  for (const state of ['failed', 'quarantined'] as const)
    assert.equal(machineStatus(state).tone, 'danger', state);
  assert.equal(machineStatus('deleted').tone, 'neutral');
  assert.equal(machineStatus('ready').tone, 'idle');
});

test('a ready machine with runners on it reads as busy, and nothing else changes with them', () => {
  // The row cannot say on its own: a host waiting for work and a host running
  // it are the same machine state, and only the runner count separates them.
  assert.equal(machineStatus('ready').key, 'ready');
  assert.equal(machineStatus('ready').tone, 'idle');
  assert.equal(machineStatus('ready', true).key, 'busy');
  assert.equal(machineStatus('ready', true).tone, 'busy');
  for (const state of MACHINE_STATES) {
    if (state === 'ready') continue;
    assert.deepEqual(machineStatus(state, true), machineStatus(state, false), state);
  }
});

test('a machine status carries tokens rather than colours, in its own tone', () => {
  // A raw colour only works in one theme. Every status hue is a token pair, so
  // a status that spelled one out would be invisible in half the deployments.
  for (const state of [...MACHINE_STATES, undefined]) {
    const status = machineStatus(state);
    assert.deepEqual(
      { colour: status.colour, subtle: status.subtle, border: status.border },
      toneTokens(status.tone),
      `${state} does not use its own tone's tokens`,
    );
    assert.match(status.colour, /^var\(--z-/);
  }
});

/* -- the band ------------------------------------------------------------- */

test('the lifecycle band counts the seven steps of the flow and leaves the exits out of it', () => {
  // Deleted machines are history, and a fleet a month old has hundreds of
  // them: a band whose largest number is the history is a band nobody reads.
  const counts = lifecycleCounts([
    machine({ state: 'creating' }),
    machine({ state: 'creating' }),
    machine({ state: 'ready' }),
    machine({ state: 'deleted' }),
    machine({ state: 'deleting' }),
    machine({ state: 'failed' }),
    machine({ state: 'quarantined' }),
  ]);
  assert.deepEqual(
    counts.map((step) => step.state),
    [...MACHINE_FLOW],
    'the band is drawn in the order the store enforces',
  );
  assert.deepEqual(
    counts.filter((step) => step.count > 0),
    [
      { state: 'creating', count: 2 },
      { state: 'ready', count: 1 },
    ],
  );
  assert.deepEqual(
    lifecycleCounts([]).map((step) => step.count),
    MACHINE_FLOW.map(() => 0),
    'an empty fleet still draws every step',
  );
});

test('a machine needs a person when nothing here will tidy it up, and not merely because it failed', () => {
  // "The state is failed" is the wrong predicate in both directions: a machine
  // whose ownership nothing could prove is running hardware the controller
  // refuses to touch, and a call that went out and was never answered may have
  // created a resource nothing has a row for.
  const listed = machinesNeedingReview([
    machine({ id: 'a', state: 'creating' }),
    machine({ id: 'b', state: 'quarantined' }),
    machine({ id: 'c', state: 'ready' }),
    machine({ id: 'd', state: 'failed' }),
    machine({ id: 'e', state: 'creating', outcome_unknown: true }),
    machine({ id: 'f', state: 'ready', ownership_error: 'the name on the VM is somebody else' }),
    machine({ id: 'g', state: 'deleted' }),
  ]);
  assert.deepEqual(
    listed.map((m) => m.id),
    ['b', 'd', 'e', 'f'],
    'in the order they were listed',
  );
  assert.equal(machineSignals(machine({ state: 'draining' }), NOW).needsReview, false);
});

test('the reason a machine needs review is the most specific one known about it', () => {
  // Three of these rows are the same shape to an operator and different
  // problems: the hypervisor refused, the guest never came up, or nobody knows.
  assert.match(
    machineSignals(machine({ state: 'quarantined' }), NOW).reviewReason,
    /Nothing could prove this resource is ours/,
  );
  assert.equal(
    machineSignals(machine({ state: 'quarantined', ownership_error: 'tag missing' }), NOW)
      .reviewReason,
    'tag missing',
  );
  assert.equal(
    machineSignals(
      machine({
        state: 'failed',
        provider_error: 'no space on pve-1',
        bootstrap_error: 'timed out',
      }),
      NOW,
    ).reviewReason,
    'no space on pve-1',
    'the hypervisor is asked before the guest',
  );
  assert.equal(
    machineSignals(machine({ state: 'failed', bootstrap_error: 'timed out' }), NOW).reviewReason,
    'timed out',
  );
  assert.match(
    machineSignals(machine({ state: 'failed' }), NOW).reviewReason,
    /never became a host/,
  );
  assert.match(
    machineSignals(machine({ state: 'creating', outcome_unknown: true }), NOW).reviewReason,
    /never answered/,
  );
});

test('a failed machine still owns the resource it was given, and a deleted one owns nothing', () => {
  // This mirrors store.Machine.Owns(), and it is the whole point of the
  // predicate: a failed machine whose VM exists is still costing money, so
  // ownership is about the resource rather than about the lifecycle.
  assert.equal(machineSignals(machine({ state: 'failed', resource_id: '143' }), NOW).owns, true);
  assert.equal(machineSignals(machine({ state: 'ready' }), NOW).owns, false);
  assert.equal(
    machineSignals(
      machine({ state: 'deleted', resource_id: '143', deleted_at: '2026-09-14T11:45:00Z' }),
      NOW,
    ).owns,
    false,
  );
  assert.equal(
    machineSignals(machine({ state: 'quarantined', resource_id: '143' }), NOW).owns,
    true,
    'a resource nobody will touch is still rented',
  );
});

test('pending is the states the fleet is still waiting on, not everything short of deleted', () => {
  const pending = MACHINE_STATES.filter((state) => machineSignals(machine({ state }), NOW).pending);
  assert.deepEqual(pending, ['planned', 'creating', 'starting', 'bootstrapping', 'enrolling']);
});

test('elapsed time is measured against a clock that is handed in, and is null when unknown', () => {
  // Nothing here reads a clock of its own, so a component and a test can agree
  // on what time it is -- and a row with no timestamp says so rather than
  // reporting an age of 1970.
  assert.equal(machineSignals(machine(), NOW).elapsedMs, 30 * 60_000);
  assert.equal(machineSignals(machine({ updated_at: '' }), NOW).elapsedMs, null);
  assert.equal(machineSignals(machine({ updated_at: 'not a date' }), NOW).elapsedMs, null);
  assert.equal(
    machineSignals(machine({ updated_at: '2026-09-14T12:30:00Z' }), NOW).elapsedMs,
    0,
    'a clock skewed forwards reads as just now rather than as negative',
  );
});

test('the operation and the delete verdict are reported exactly as the row gives them', () => {
  const idle = machineSignals(machine(), NOW);
  assert.equal(idle.operation, '');
  assert.equal(idle.safeToDelete, false);
  assert.equal(idle.safeToDeleteWhy, '');
  const busy = machineSignals(
    machine({ operation: 'create', safe_to_delete: true, safe_to_delete_why: 'nothing on it' }),
    NOW,
  );
  assert.equal(busy.operation, 'create');
  assert.equal(busy.safeToDelete, true);
  assert.equal(busy.safeToDeleteWhy, 'nothing on it');
});

/* -- the timeline --------------------------------------------------------- */

test('a timeline row says which half of a slow step is slow, and names a phase it has no sentence for', () => {
  // "creating" and "created" are two marks on one state: a clone that never
  // finished is a problem at the hypervisor, and a machine that cloned and
  // never enrolled is a problem inside the guest. Both rows are drawn in the
  // one state's colour, so the diagnosis is in the words rather than the hue.
  assert.equal(phaseLabel('creating'), 'Creating the machine');
  assert.equal(phaseLabel('created'), 'Machine created');
  assert.equal(phaseState('creating'), 'creating');
  assert.equal(phaseState('created'), 'creating');
  assert.equal(phaseState('bootstrapped'), 'bootstrapping');
  assert.equal(phaseState('enrolled'), 'enrolling');

  assert.equal(phaseLabel(undefined), 'Unknown');
  assert.equal(phaseLabel('resized'), 'Resized', 'a phase we have no sentence for is still shown');
  assert.equal(phaseState(undefined), undefined);
  assert.equal(phaseState('resized'), undefined);
  assert.equal(machineStatus(phaseState('resized')).key, 'unknown');
});

/* -- what it costs -------------------------------------------------------- */

test('a provider with no hourly rate reports no cost at all rather than a free one', () => {
  // A hypervisor in a cupboard has no rate, and "0.00" is a claim about money
  // that nobody made.
  const m = machine({ created_at: '2026-09-14T11:00:00Z' });
  assert.equal(machineCost(m, undefined, NOW), null);
  assert.equal(machineCost(m, { cost_per_machine_hour: 0 }, NOW), null);
  assert.equal(machineCost(m, { cost_per_machine_hour: -1 }, NOW), null);
  assert.equal(formatCost(null), '');
  assert.equal(formatCost(0), '0.00', 'a rate that really is zero prints as a figure');
  assert.equal(formatCost(1.5), '1.50');
});

test('a machine costs from when it was created until it was deleted, not until now', () => {
  const rate: Pick<Provider, 'cost_per_machine_hour'> = { cost_per_machine_hour: 4 };
  assert.equal(machineCost(machine(), rate, NOW), 4, 'an hour old, still running');
  assert.equal(
    machineCost(machine({ deleted_at: '2026-09-14T11:30:00Z' }), rate, NOW),
    2,
    'billing stops when the provider confirmed the resource was gone',
  );
  assert.equal(machineCost(machine({ created_at: '' }), rate, NOW), null);
  assert.equal(machineCost(machine({ deleted_at: 'not a date' }), rate, NOW), null);
});

/* -- the wizard draft ----------------------------------------------------- */

test('a draft holds its numbers as strings, so an empty box and a deliberate zero stay apart', () => {
  // This is the whole reason the draft is not typed with numbers: coercing
  // here would turn "not filled in" into a deliberate zero, and
  // `max_machines: 0` is the setting that rents nothing.
  const draft = emptyDraft();
  assert.equal(draft.max_machines, '0', 'the safe default is a deliberate zero');
  assert.equal(draft.cost_per_machine_hour, '', 'and an unset price is not a free one');
  assert.equal(typeof draft.max_machines, 'string');
  assert.equal(typeof draft.cost_per_machine_hour, 'string');

  assert.equal(toProviderBody(draft).max_machines, 0);
  assert.equal(
    'cost_per_machine_hour' in toProviderBody(draft),
    false,
    'an unset price is left out of the body rather than sent as zero',
  );
  assert.equal(
    toProviderBody({ ...draft, cost_per_machine_hour: '0' }).cost_per_machine_hour,
    0,
    'and a price of zero, typed on purpose, is sent',
  );
});

test('a provider round-trips through a draft and back, without ever carrying its credential', () => {
  // The controller seals the credential and never gives it back, so the box is
  // empty on an edit and an empty box has to mean "leave the stored one alone".
  const provider: Provider = {
    id: 'prv_k3f9qz2mx7ab',
    kind: 'proxmox',
    name: 'pve',
    endpoint: 'https://pve.example.com:8006',
    insecure_skip_verify: true,
    settings: { node: 'pve-1', template: '9000' },
    machine_labels: { zone: 'cupboard' },
    machine_capacity: 4,
    machine_backend: 'docker',
    machine_platform: { os: 'ubuntu', os_version: '24.04', arch: 'amd64' },
    machine_cpus: 8,
    machine_memory_mb: 16384,
    machine_disk_mb: 65536,
    max_machines: 6,
    max_creates_in_flight: 2,
    idle_timeout_ms: 900_000,
    cost_per_machine_hour: 0.08,
    enabled: true,
  } as Provider;

  const draft = draftFromProvider(provider);
  assert.equal(draft.credential, '', 'never populated from a provider that already exists');
  assert.equal(draft.idle_timeout, '15m', 'milliseconds come back in the units they were typed in');
  assert.equal(draft.machine_os_version, '24.04');
  assert.notEqual(draft.settings, provider.settings, 'the draft owns its own copy');

  const body = toProviderBody(draft);
  assert.equal('credential' in body, false, 'an empty box never erases a working credential');
  assert.deepEqual(body.settings, { node: 'pve-1', template: '9000' });
  assert.deepEqual(body.machine_platform, { os: 'ubuntu', os_version: '24.04', arch: 'amd64' });
  assert.equal(body.machine_capacity, 4);
  assert.equal(body.machine_cpus, 8);
  assert.equal(body.max_machines, 6);
  assert.equal(body.max_creates_in_flight, 2);
  assert.equal(body.idle_timeout, '15m');
  assert.equal(body.cost_per_machine_hour, 0.08);
  assert.equal(body.enabled, true);
  assert.equal(body.insecure_skip_verify, true);
  assert.equal(body.kind, 'proxmox');

  assert.equal(toProviderBody({ ...draft, credential: ' secret ' }).credential, ' secret ');
});

test('a provider that says nothing about a setting takes the draft default rather than an empty box', () => {
  const draft = draftFromProvider({ id: 'prv_1', kind: 'proxmox', name: 'pve' } as Provider);
  assert.equal(draft.machine_capacity, '2');
  assert.equal(draft.max_machines, '0');
  assert.equal(draft.max_creates_in_flight, '1');
  assert.equal(draft.idle_timeout, '15m');
  assert.equal(draft.machine_backend, 'docker');
  assert.equal(draft.enabled, true, 'a provider that does not say is enabled');
  assert.equal(draftFromProvider({ enabled: false } as Provider).enabled, false);
  assert.equal(draft.machine_cpus, '', 'but a size nobody set stays unset');
});

test('a field an operator cleared is only cleared on the wire when the whole body is sent', () => {
  // A PATCH leaves what it does not name alone, so on an edit an emptied box
  // has to be sent as its empty value or clearing it would do nothing at all.
  const cleared = { ...emptyDraft(), kind: 'proxmox', name: 'pve', machine_cpus: '' };
  const partial = toProviderBody(cleared);
  assert.equal('machine_cpus' in partial, false);
  assert.equal('ca_pem' in partial, false);
  assert.equal('machine_platform' in partial, false);
  assert.equal('idle_timeout' in partial, true, 'the draft default is not empty');

  const complete = toProviderBody({ ...cleared, idle_timeout: '' }, { complete: true });
  assert.equal(complete.machine_cpus, 0);
  assert.equal(complete.machine_memory_mb, 0);
  assert.equal(complete.machine_disk_mb, 0);
  assert.equal(complete.cost_per_machine_hour, 0);
  assert.equal(complete.ca_pem, '');
  assert.deepEqual(complete.machine_platform, {});
  assert.equal(complete.idle_timeout, '');
  assert.equal(
    'credential' in complete,
    false,
    'the credential is the one exception in both directions',
  );
});

test('settings and labels are trimmed on the way out, and a nameless one is dropped', () => {
  const body = toProviderBody({
    ...emptyDraft(),
    settings: { ' node ': ' pve-1 ', '': 'orphan', keep: '' },
    machine_labels: { ' zone ': ' cupboard ' },
    name: '  pve  ',
    endpoint: '  https://pve.example.com:8006  ',
  });
  assert.deepEqual(body.settings, { node: 'pve-1', keep: '' });
  assert.deepEqual(body.machine_labels, { zone: 'cupboard' });
  assert.equal(body.name, 'pve');
  assert.equal(body.endpoint, 'https://pve.example.com:8006');
});

test('a blank count falls back to the safe answer rather than to nothing at all', () => {
  // `machine_capacity` is required by the contract, so a cleared box has to
  // become a number: two runner slots, not zero, which is a machine that can
  // never be scheduled on.
  const blank = {
    ...emptyDraft(),
    machine_capacity: '',
    max_machines: '',
    max_creates_in_flight: '',
  };
  const body = toProviderBody(blank);
  assert.equal(body.machine_capacity, 2);
  assert.equal(body.max_machines, 0, 'rent nothing');
  assert.equal(body.max_creates_in_flight, 1);
});

test('a setting the operator never touched takes the driver default, and one they filled in is left alone', () => {
  // A form that comes filled in is the difference between a wizard and an
  // interrogation, but a default that overwrote an answer would be worse than
  // no default at all.
  const specs = [
    spec({ key: 'node', default: 'pve-1' }),
    spec({ key: 'storage', default: 'local-lvm' }),
    spec({ key: 'template' }),
  ];
  const draft = { ...emptyDraft(), settings: { storage: 'ceph' } };
  const filled = applySettingDefaults(draft, specs);
  assert.deepEqual(filled.settings, { node: 'pve-1', storage: 'ceph' });
  assert.equal(
    'template' in filled.settings,
    false,
    'a setting the driver has no default for stays absent',
  );
  assert.equal(
    applySettingDefaults(filled, specs),
    filled,
    'a second pass changes nothing and returns the same draft, so a form cannot loop on it',
  );
  assert.deepEqual(
    applySettingDefaults({ ...emptyDraft(), settings: { node: '' } }, specs).settings.node,
    'pve-1',
    'an empty box is not an answer, so the default still lands',
  );
});

/* -- where a rejection lands ---------------------------------------------- */

test('every field the wizard names has a step to land on and a label to be named by', () => {
  // A rejection the form cannot place is a rejection the operator never sees:
  // stepForField indexes STEP_FIELDS into WIZARD_STEPS, so the two lists have
  // to stay the same length as steps are added.
  assert.equal(STEP_FIELDS.length, WIZARD_STEPS.length);
  for (const fields of STEP_FIELDS)
    for (const field of fields) assert.ok(FIELD_LABELS[field], `${field} has no label`);
});

test("a driver's own setting lands on the placement step, and an unknown field on review", () => {
  assert.equal(WIZARD_STEPS[stepForField('settings.node')]?.id, 'placement');
  assert.equal(WIZARD_STEPS[stepForField('settings.anything_at_all')]?.id, 'placement');
  assert.equal(WIZARD_STEPS[stepForField('kind')]?.id, 'connect');
  assert.equal(WIZARD_STEPS[stepForField('machine_cpus')]?.id, 'machine');
  assert.equal(WIZARD_STEPS[stepForField('max_machines')]?.id, 'limits');
  assert.equal(
    WIZARD_STEPS[stepForField('something_the_server_added')]?.id,
    'review',
    'where every message is shown at once',
  );
});

/* -- what the form refuses ------------------------------------------------ */

function valid(): ReturnType<typeof emptyDraft> {
  return {
    ...emptyDraft(),
    kind: 'proxmox',
    name: 'pve',
    endpoint: 'https://pve.example.com:8006',
    credential: 'root@pam!zoomies=secret',
  };
}

test('a complete draft is accepted, and every missing answer is reported under its own field', () => {
  assert.deepEqual(draftErrors(valid(), []), {});
  const empty = draftErrors(emptyDraft(), []);
  assert.deepEqual(Object.keys(empty).sort(), ['credential', 'endpoint', 'kind', 'name']);
});

test('plain HTTP off the box is refused, and the certificate switch is not a way round it', () => {
  // The credential on this row can create and destroy machines, and turning
  // certificate verification off is an answer to a different question. The
  // driver refuses this address at its first call whatever the switch says --
  // and by then the row is saved, the check answers 500 with the reason only in
  // the log, and machines sit in `planned` with nothing on any page saying why.
  // So the form must not offer the switch as an override it cannot honour.
  const http = { ...valid(), endpoint: 'http://pve.example.com:8006' };
  for (const insecure of [false, true]) {
    const got = draftErrors({ ...http, insecure_skip_verify: insecure }, []).endpoint ?? '';
    assert.match(got, /in the clear/);
    assert.match(got, /does not permit this/);
  }
  // Loopback carries nothing off the box, so it stays allowed.
  for (const endpoint of ['http://localhost:8006', 'http://127.0.0.1:8006', 'http://[::1]:8006']) {
    assert.equal(draftErrors({ ...valid(), endpoint }, []).endpoint, undefined, endpoint);
  }
  assert.match(
    draftErrors({ ...valid(), endpoint: 'pve.example.com' }, []).endpoint ?? '',
    /scheme/,
  );
});

test('a credential is required on the way in and optional on an edit', () => {
  // An empty box on an edit means "leave the stored one alone", so demanding
  // one there would make every edit a credential rotation.
  const blank = { ...valid(), credential: '   ' };
  assert.match(draftErrors(blank, []).credential ?? '', /credential is needed/);
  assert.equal(draftErrors(blank, [], { editing: true }).credential, undefined);
});

test("a driver's settings are checked against its own schema, under the API's field names", () => {
  const specs = [
    spec({ key: 'node', label: 'Node', required: true }),
    spec({ key: 'cores', label: 'Cores', kind: 'number' }),
  ];
  const errors = draftErrors({ ...valid(), settings: { cores: 'lots' } }, specs);
  assert.equal(errors['settings.node'], 'Node is needed.');
  assert.equal(errors['settings.cores'], 'Cores has to be a number.');
  assert.deepEqual(draftErrors({ ...valid(), settings: { node: 'pve-1', cores: '4' } }, specs), {});
  assert.equal(
    draftErrors({ ...valid(), settings: { node: 'pve-1', cores: '  ' } }, specs)['settings.cores'],
    undefined,
    'an optional number left empty is an answer',
  );
});

test('a ceiling of zero is legal but a machine with no slots, or none built at once, is not', () => {
  // Zero machines is the default and the safe one. Zero runner slots, or zero
  // builds in flight, are the two numbers that read as a limit and mean
  // "nothing will ever happen" -- so those are errors and the ceiling is not.
  assert.equal(draftErrors({ ...valid(), max_machines: '0' }, []).max_machines, undefined);
  assert.match(draftErrors({ ...valid(), max_machines: '2.5' }, []).max_machines ?? '', /whole/);
  assert.ok(draftErrors({ ...valid(), max_machines: '-1' }, []).max_machines);
  assert.ok(draftErrors({ ...valid(), max_machines: '' }, []).max_machines);
  assert.ok(draftErrors({ ...valid(), max_creates_in_flight: '0' }, []).max_creates_in_flight);
  assert.ok(draftErrors({ ...valid(), machine_capacity: '0' }, []).machine_capacity);
  assert.ok(draftErrors({ ...valid(), machine_capacity: '' }, []).machine_capacity);
});

test('a machine size left empty is fine and a negative one is not', () => {
  for (const field of [
    'machine_cpus',
    'machine_memory_mb',
    'machine_disk_mb',
    'cost_per_machine_hour',
  ] as const) {
    assert.equal(draftErrors({ ...valid(), [field]: '' }, [])[field], undefined, field);
    assert.ok(draftErrors({ ...valid(), [field]: '-1' }, [])[field], field);
    assert.ok(draftErrors({ ...valid(), [field]: 'lots' }, [])[field], field);
    assert.equal(draftErrors({ ...valid(), [field]: '0' }, [])[field], undefined, field);
  }
});

test('the idle timeout is a Go duration, or nothing', () => {
  assert.equal(draftErrors({ ...valid(), idle_timeout: '' }, []).idle_timeout, undefined);
  for (const ok of ['15m', '1h', '90s', '1.5h'])
    assert.equal(draftErrors({ ...valid(), idle_timeout: ok }, []).idle_timeout, undefined, ok);
  for (const bad of ['15 minutes', '15', 'm', '15M'])
    assert.ok(draftErrors({ ...valid(), idle_timeout: bad }, []).idle_timeout, bad);
});

test('a provider that would rent nothing says so as a note beside the setting, not as an error', () => {
  // Saving a provider with a ceiling of zero is a legal answer and the default
  // one -- the operator is meant to connect it first and raise it deliberately.
  assert.match(willRentNothing(emptyDraft()), /ceiling is zero/);
  assert.match(willRentNothing({ ...emptyDraft(), enabled: false }), /switched off/);
  assert.equal(willRentNothing({ ...emptyDraft(), max_machines: '3' }), '');
  assert.equal(draftErrors({ ...valid(), max_machines: '0' }, []).max_machines, undefined);
});

test('a private connection address goes out once and a blank box on an edit keeps the stored one', () => {
  const fresh = {
    ...emptyDraft(),
    kind: 'proxmox',
    name: 'home',
    endpoint: 'https://pve.lan:8006',
  };
  assert.equal(toProviderBody(fresh).connection, 'direct');
  assert.equal('tailcat_address' in toProviderBody(fresh), false);

  const errors = draftErrors({ ...fresh, connection: 'tailcat', credential: 'x' }, []);
  assert.ok(errors.tailcat_address, 'a private connection with no address is not one');
  assert.equal(
    draftErrors(
      { ...fresh, connection: 'tailcat', credential: 'x', tailcat_address: 'not it' },
      [],
    ).tailcat_address?.includes('beginning with tc'),
    true,
  );
  const address = 'tc' + 'a'.repeat(40);
  const body = toProviderBody({ ...fresh, connection: 'tailcat', tailcat_address: ` ${address} ` });
  assert.equal(body.connection, 'tailcat');
  assert.equal(body.tailcat_address, address);

  // An existing private provider comes back without its address, and an edit
  // that leaves the box blank is valid and sends no address at all.
  const stored = draftFromProvider({
    id: 'prv_1',
    kind: 'proxmox',
    name: 'home',
    endpoint: 'https://pve.lan:8006',
    connection: 'tailcat',
  } as Provider);
  assert.equal(stored.connection, 'tailcat');
  assert.equal(stored.tailcat_configured, true);
  assert.equal(stored.tailcat_address, '');
  assert.equal(draftErrors(stored, [], { editing: true }).tailcat_address, undefined);
  assert.equal('tailcat_address' in toProviderBody(stored), false);
  // Switching back to direct is the deliberate act that clears it, and an
  // address typed under direct is never sent.
  const direct = toProviderBody({ ...stored, connection: 'direct', tailcat_address: address });
  assert.equal(direct.connection, 'direct');
  assert.equal('tailcat_address' in direct, false);
});
