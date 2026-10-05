import { test } from 'node:test';
import assert from 'node:assert/strict';
import { backendSwitch } from '../src/lib/pools/backend-switch.ts';
import type { Pool } from '../src/lib/api/types.ts';

const pool = (over: Partial<Pool>): Pool => ({ backend: 'docker', ...over }) as Pool;

// The one-click remedy for a pool no host can run has to be a request the API
// accepts, and the API refuses a pool on the process backend that still has
// elastic CPU or the memory valve on.
test('moving to the process backend turns off what a process cannot have', () => {
  const { body, switchedOff } = backendSwitch(
    pool({
      docker_mode: 'dind',
      cpu_burst: { mode: 'observe', max_cpus: 6, size_for_ceiling: false },
      memory_burst: { mode: 'automatic', max_memory_mb: 8192, spill_mb: 1024 },
    }),
    'process',
  );
  assert.equal(body.backend, 'process');
  assert.equal(body.docker_mode, 'none');
  assert.deepEqual(body.memory_burst, { mode: 'off' });
  assert.equal(body.cpu_burst?.mode, 'off');
  assert.equal(body.cpu_burst?.size_for_ceiling, false);
  assert.equal(switchedOff.length, 2);
  assert.match(switchedOff[0] ?? '', /Docker for jobs is switched off/);
  assert.match(switchedOff[1] ?? '', /^Elastic CPU and the memory valve are switched off/);
});

test('a pool that has neither is sent only what it always was, and told nothing extra', () => {
  const { body, switchedOff } = backendSwitch(
    pool({
      docker_mode: 'none',
      cpu_burst: { mode: 'off', size_for_ceiling: true },
      memory_burst: { mode: 'off' },
    }),
    'process',
  );
  assert.deepEqual(body, { backend: 'process', docker_mode: 'none' });
  assert.deepEqual(switchedOff, []);
});

test('each of the two is named alone when only one was on', () => {
  const memory = backendSwitch(pool({ memory_burst: { mode: 'observe' } }), 'process');
  assert.deepEqual(memory.switchedOff, [
    'The memory valve is switched off, because a process has no container limits to measure or move.',
  ]);
  assert.equal(memory.body.cpu_burst, undefined);
  const cpu = backendSwitch(
    pool({ cpu_burst: { mode: 'automatic', size_for_ceiling: true } }),
    'process',
  );
  assert.match(cpu.switchedOff[0] ?? '', /^Elastic CPU is switched off/);
  assert.equal(cpu.body.memory_burst, undefined);
});

test('moving between container backends changes the backend and nothing else', () => {
  const { body, switchedOff } = backendSwitch(
    pool({
      memory_burst: { mode: 'automatic' },
      cpu_burst: { mode: 'automatic', size_for_ceiling: true },
    }),
    'podman',
  );
  assert.deepEqual(body, { backend: 'podman' });
  assert.deepEqual(switchedOff, []);
});
