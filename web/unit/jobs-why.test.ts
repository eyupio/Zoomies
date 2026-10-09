import assert from 'node:assert/strict';
import test from 'node:test';
import { evidenceRows, stepAction } from '../src/lib/jobs/why.ts';

// A next step is a button where an action exists and a row where it does
// not: a re-run is the drawer's own button, a path in this UI is a link that
// stays here, a documentation URL opens elsewhere, and a bare sentence is
// something to do by hand.
test('a next step becomes the action its kind and link allow', () => {
  assert.deepEqual(stepAction({ text: 'Re-run the job.', kind: 'rerun' }), {
    label: 'Re-run the job.',
    rerun: true,
  });
  assert.deepEqual(stepAction({ text: 'Open the pool.', kind: 'read', link: '/pools/pool_1' }), {
    label: 'Open the pool.',
    href: '/pools/pool_1',
    external: false,
  });
  assert.deepEqual(
    stepAction({ text: 'Read on.', kind: 'read', link: 'https://zoomies.sh/problem-codes/' }),
    { label: 'Read on.', href: 'https://zoomies.sh/problem-codes/', external: true },
  );
  assert.deepEqual(stepAction({ text: 'Raise max_runners.', kind: 'change' }), {
    label: 'Raise max_runners.',
  });
});

// Figures read in a monospace column so two of them line up; words do not.
// The order is the controller's, which put the deciding fact first.
test('evidence keeps its order and marks the figures', () => {
  const rows = evidenceRows([
    {
      kind: 'conclusion',
      label: "GitHub's conclusion",
      value: 'failure',
      ref: 'https://github.com/x',
    },
    { kind: 'exit_code', label: 'exit code', value: '137' },
    { kind: 'memory_peak', label: 'memory peak', value: '7900', unit: 'MB' },
    { kind: 'queue_wait', label: 'queue wait', value: '42', unit: 's' },
    { kind: 'host_state', label: 'host', value: 'healthy', ref: '/hosts/host_1' },
  ]);
  assert.deepEqual(
    rows.map((r) => [r.label, r.value, r.mono, r.href ?? null]),
    [
      ["GitHub's conclusion", 'failure', false, 'https://github.com/x'],
      ['exit code', '137', true, null],
      ['memory peak', '7900 MB', true, null],
      ['queue wait', '42 s', true, null],
      ['host', 'healthy', false, '/hosts/host_1'],
    ],
  );
});
