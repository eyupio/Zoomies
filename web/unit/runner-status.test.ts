import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { registerHooks } from 'node:module';

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

const { runnerDisplayStatus } = await import('../src/lib/runners/runner-status.ts');
const { runnerStatus, runnerStatuses } = await import('../src/lib/status.ts');

test('active boosts and throttles replace busy or idle but retain lifecycle detail', () => {
  for (const state of ['busy', 'idle'] as const) {
    for (const [cpu, label] of [
      ['maximum_zoomies', 'Squirrel spotted'],
      ['zoomies', 'Rabbit spotted'],
      ['throttled', 'Leash tightened'],
    ] as const) {
      const status = runnerDisplayStatus({ state, cpu_resource: { state: cpu } });
      assert.equal(status.label, label);
      assert.equal(status.lifecycle.key, state);
      assert.equal(status.active, true);
    }
  }
});

test('stale CPU allocation never masks startup, draining or terminal states', () => {
  for (const state of ['provisioning', 'registering', 'draining', 'failed', 'removed'] as const) {
    for (const cpu of ['maximum_zoomies', 'zoomies', 'throttled'] as const) {
      const status = runnerDisplayStatus({ state, cpu_resource: { state: cpu } });
      assert.equal(status.key, state);
      assert.equal(status.active, false);
    }
  }
});

test('observation and normal allocations keep the lifecycle label', () => {
  // The lifecycle's own word, read from the state map rather than written
  // here: what is being tested is that a quiet allocation does not replace it,
  // not what the word is, and the runner vocabulary has already changed once
  // under a test that spelt it out.
  for (const cpu of ['observing', 'guaranteed', 'sit_and_stay'] as const) {
    const status = runnerDisplayStatus({ state: 'busy', cpu_resource: { state: cpu } });
    assert.equal(status.label, runnerStatus('busy').label);
    assert.ok(status.cpuDetail);
  }
  assert.equal(
    runnerDisplayStatus({ state: 'registering' }).label,
    runnerStatus('registering').label,
  );
  assert.equal(runnerDisplayStatus({}).label, 'Unknown');
});

test('turning the kennel vocabulary off gives every state a plain label, lifecycle and boosted alike', () => {
  assert.equal(runnerStatus('provisioning', false).label, 'Provisioning');
  assert.equal(runnerStatus('idle', false).label, 'Idle');
  for (const state of ['busy', 'idle'] as const) {
    for (const [cpu, label] of [
      ['maximum_zoomies', 'Zoomies'],
      ['zoomies', 'Extra'],
      ['throttled', 'Throttled'],
    ] as const) {
      const status = runnerDisplayStatus({ state, cpu_resource: { state: cpu } }, false);
      assert.equal(status.label, label);
      assert.equal(status.active, true);
    }
  }
  // The default stays the kennel vocabulary: a caller that does not say
  // otherwise gets what the product has always shown.
  assert.equal(
    runnerDisplayStatus({ state: 'busy', cpu_resource: { state: 'maximum_zoomies' } }).label,
    'Squirrel spotted',
  );
});

/*
 * The Runners grid's own buttons: Drain and Delete. A state drawn with one of
 * them, or with a media control, reads as something to press -- busy runners
 * drawn with Play looked like runners waiting to be started.
 */
const BUTTON_ICONS = ['Play', 'Pause', 'CircleSlash', 'Trash2'];

test('with the vocabulary off, no runner state is drawn with an icon a button uses', () => {
  const drawn = [
    ...runnerStatuses(false).map((status) => [status.key, status.icon.name]),
    ...(['maximum_zoomies', 'zoomies', 'throttled'] as const).map((cpu) => {
      const status = runnerDisplayStatus({ state: 'busy', cpu_resource: { state: cpu } }, false);
      return [status.key, status.icon.name];
    }),
  ];
  for (const [key, icon] of drawn) {
    assert.ok(!BUTTON_ICONS.includes(icon), `${key} is drawn with ${icon}`);
  }
});

test('every runner state has an icon of its own', () => {
  // Provisioning and registering were one dashed ring, and a runner stuck on
  // GitHub's side looked exactly like one still being built on the host's.
  const icons = runnerStatuses(false).map((status) => status.icon.name);
  assert.equal(new Set(icons).size, icons.length, icons.join(', '));
  assert.equal(runnerStatus('busy', false).icon.name, 'Activity');
});

test('the two boosts and the throttle are told apart by label and by icon', () => {
  // Both boosts said "Boost active" in the grid with the vocabulary off, and
  // the icon alone did not tell an operator which was extra and which maximum.
  const shown = (cpu: string) =>
    runnerDisplayStatus({ state: 'busy', cpu_resource: { state: cpu } }, false);
  const states = [shown('maximum_zoomies'), shown('zoomies'), shown('throttled')];
  const labels = states.map((status) => status.label);
  const icons = states.map((status) => status.icon.name);
  assert.equal(new Set(labels).size, 3, labels.join(', '));
  assert.equal(new Set(icons).size, 3, icons.join(', '));
});
