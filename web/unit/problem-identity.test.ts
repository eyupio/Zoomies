import { test } from 'node:test';
import assert from 'node:assert/strict';
import { problemKey } from '../src/lib/problems/identity.ts';
import type { Problem } from '../src/lib/api/types.ts';

const problem = (over: Partial<Problem> = {}): Problem => ({
  code: 'pool.no_capacity',
  severity: 'error',
  title: 'pool zoomies-linux-x64 has 21 jobs waiting and nowhere to run them',
  target_kind: 'pool',
  target_id: 'pool_x64',
  ...over,
});

// The count in the title moves every reconciliation pass. If it were part of
// the key, a snooze made at 21 jobs would stop covering the same outage the
// moment a 22nd job queued -- the exact nagging the dismissals exist to stop.
test('a pool.no_capacity problem keeps its identity as the queue length changes', () => {
  const before = problemKey(
    problem({ title: 'pool zoomies-linux-x64 has 21 jobs waiting and nowhere to run them' }),
  );
  const after = problemKey(
    problem({ title: 'pool zoomies-linux-x64 has 22 jobs waiting and nowhere to run them' }),
  );
  assert.equal(before, after);
});

// pool.dangerous is raised once per weakened setting on the same pool, so two
// of them share a code and a target and the title is the only thing telling
// them apart -- folding the title out of the key entirely would collapse
// "host docker socket mounted" and "runners execute as root" into one problem.
test('two pool.dangerous problems on the same pool keep separate identities', () => {
  const socket = problemKey(
    problem({
      code: 'pool.dangerous',
      title: 'pool spaniel: host docker socket mounted: any job on this pool can become root',
    }),
  );
  const root = problemKey(
    problem({
      code: 'pool.dangerous',
      title: 'pool spaniel: runners execute as root inside the container',
    }),
  );
  assert.notEqual(socket, root);
});

// A host with both a docker and a podman daemon that cannot apply limits (or
// cannot mount the shared folder) raises the same code twice against one host.
// The backend in the title is all that separates them, so muting docker's must
// not mute podman's.
test('the same host problem raised for two backends keeps separate identities', () => {
  for (const code of ['host.limits_unenforceable', 'host.shared_folder_unmounted']) {
    const docker = problemKey(
      problem({ code, target_kind: 'host', target_id: 'host_a', title: 'a docker problem' }),
    );
    const podman = problemKey(
      problem({ code, target_kind: 'host', target_id: 'host_a', title: 'a podman problem' }),
    );
    assert.notEqual(docker, podman, code);
  }
});

test('two problems for different pools never collide regardless of code', () => {
  const first = problemKey(problem({ target_id: 'pool_x64' }));
  const second = problemKey(problem({ target_id: 'pool_arm64' }));
  assert.notEqual(first, second);
});

// A snooze of "all like this" has to cover a pool that runs short after the
// snooze was made, so its key is the code alone -- and it must never be
// mistaken for one problem's key, or restoring one would lift the other.
test('every problem of one code shares a type key that no problem key equals', async () => {
  const { problemTypeKey } = await import('../src/lib/problems/identity.ts');
  const a = problem({ target_id: 'pool_x64' });
  const b = problem({ target_id: 'pool_arm64', title: 'another title entirely' });
  assert.equal(problemTypeKey(a), problemTypeKey(b));
  assert.notEqual(problemTypeKey(a), problemTypeKey(problem({ code: 'host.unhealthy' })));
  assert.notEqual(problemTypeKey(a), problemKey(a));
});
