/**
 * What it takes to move a pool to another backend in one request.
 *
 * Moving to the process backend is more than naming it. A process has no Docker
 * daemon to give a job, and no container whose limits can be measured or moved,
 * so a pool that has Docker for jobs, elastic CPU or the memory valve on loses
 * each in the same change -- and the API refuses a pool that keeps them, with an
 * error on a field the one-click remedy never showed. Working the change out in
 * one place says what is switched off both in the request and in the sentence
 * that asks for confirmation, so the two cannot disagree.
 */
import type { BackendKind, Body, Pool } from '$lib/api/types';

export interface BackendSwitch {
  /** The request body: the new backend, and whatever has to go with it. */
  body: Body<'updatePool'>;
  /** What the change switches off, as a sentence each, for the confirmation. */
  switchedOff: string[];
}

const on = (mode: string | undefined): boolean =>
  mode !== undefined && mode !== '' && mode !== 'off';

export function backendSwitch(pool: Pool, backend: BackendKind): BackendSwitch {
  const body: Body<'updatePool'> = { backend };
  const switchedOff: string[] = [];
  if (backend !== 'process') return { body, switchedOff };

  body.docker_mode = 'none';
  if (pool.docker_mode !== undefined && pool.docker_mode !== 'none') {
    switchedOff.push(
      'Docker for jobs is switched off, because the process backend cannot provide it.',
    );
  }

  const cpu = on(pool.cpu_burst?.mode);
  const memory = on(pool.memory_burst?.mode);
  if (cpu) {
    body.cpu_burst = {
      mode: 'off',
      max_cpus: 0,
      size_for_ceiling: pool.cpu_burst?.size_for_ceiling ?? true,
    };
  }
  if (memory) body.memory_burst = { mode: 'off' };
  const because = 'because a process has no container limits to measure or move.';
  if (cpu && memory)
    switchedOff.push(`Elastic CPU and the memory valve are switched off, ${because}`);
  else if (cpu) switchedOff.push(`Elastic CPU is switched off, ${because}`);
  else if (memory) switchedOff.push(`The memory valve is switched off, ${because}`);
  return { body, switchedOff };
}
