import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  NO_FIGURES,
  figuresOf,
  isUnset,
  machineOf,
  profileBody,
  profileErrors,
  sameFigures,
  slotsFor,
} from '../src/lib/hosts/profile.ts';
import { slotsOf } from '../src/lib/hosts/slots.ts';

// The machines the docs' mixed-fleet example uses: a 12-CPU host and a 4-CPU
// host, each less the reserve the controller holds back.
const big = { cpus: 12, memoryMb: 32768, allocatableCpus: 11.4, allocatableMemoryMb: 31130 };
const small = { cpus: 4, memoryMb: 16384, allocatableCpus: 3.5, allocatableMemoryMb: 15565 };

test('a stored profile reads back into the form, and an empty one is every figure at zero', () => {
  assert.deepEqual(figuresOf(undefined), NO_FIGURES);
  assert.deepEqual(figuresOf({}), NO_FIGURES);
  assert.deepEqual(
    figuresOf({
      minimum: { cpus: 2, memory_mb: 4096 },
      standard: { cpus: 3, memory_mb: 8192, burst_max_cpus: 6, burst_max_memory_mb: 12288 },
    }),
    {
      minCpus: 2,
      minMemoryMb: 4096,
      standardCpus: 3,
      standardMemoryMb: 8192,
      burstMaxCpus: 6,
      burstMaxMemoryMb: 12288,
      tmpfsOff: false,
      tmpfsMaxMb: 0,
      tmpfsWorkMb: 0,
      tmpfsTmpMb: 0,
      tmpfsDaemonMb: 0,
    },
  );
});

// The API replaces the whole profile with what it is sent. A field that is
// zero in the form has to be absent from the request, or the host would be
// saved with an explicit zero the server refuses, and an empty form has to be
// `{}` because that is how "follow the fleet everywhere" is said.
test('the request carries only the figures that are set, and {} where none is', () => {
  assert.deepEqual(profileBody(NO_FIGURES), {});
  assert.deepEqual(profileBody({ ...NO_FIGURES, standardCpus: 3 }), { standard: { cpus: 3 } });
  assert.deepEqual(profileBody({ ...NO_FIGURES, minMemoryMb: 2048, burstMaxCpus: 4 }), {
    minimum: { memory_mb: 2048 },
    standard: { burst_max_cpus: 4 },
  });
  const full = {
    minCpus: 1,
    minMemoryMb: 2048,
    standardCpus: 3,
    standardMemoryMb: 8192,
    burstMaxCpus: 6,
    burstMaxMemoryMb: 12288,
    tmpfsOff: false,
    tmpfsMaxMb: 0,
    tmpfsWorkMb: 0,
    tmpfsTmpMb: 0,
    tmpfsDaemonMb: 0,
  };
  assert.deepEqual(figuresOf(profileBody(full)), full);
  assert.ok(isUnset(NO_FIGURES));
  assert.ok(!isUnset({ ...NO_FIGURES, burstMaxCpus: 1 }));
  assert.ok(
    !isUnset({ ...NO_FIGURES, burstMaxMemoryMb: 4096 }),
    'a memory ceiling alone is a profile',
  );
  assert.ok(sameFigures(full, { ...full }));
  assert.ok(!sameFigures(full, { ...full, minCpus: 2 }));
  assert.ok(!sameFigures(full, { ...full, burstMaxMemoryMb: 16384 }));
});

test('a memory ceiling is sent with the standard size, where the API keeps it', () => {
  assert.deepEqual(profileBody({ ...NO_FIGURES, burstMaxMemoryMb: 16384 }), {
    standard: { burst_max_memory_mb: 16384 },
  });
  assert.deepEqual(
    profileBody({ ...NO_FIGURES, standardMemoryMb: 8192, burstMaxMemoryMb: 16384 }),
    {
      standard: { memory_mb: 8192, burst_max_memory_mb: 16384 },
    },
  );
});

test('a host takes as many runners of its standard size as its machine holds', () => {
  // 11.4 CPUs hold three runners of 3 and 31130 MB holds three of 8 GB: the
  // cores run out first, by the controller's own tie-break.
  assert.deepEqual(slotsFor(big, 8, { ...NO_FIGURES, standardCpus: 3, standardMemoryMb: 8192 }), {
    slots: 3,
    held: 3,
    limitedBy: 'cpu',
    derived: true,
  });
  // Two runners of 1.5 CPU and 4 GB on a small host: 3.5/1.5 and 15565/4096.
  assert.deepEqual(
    slotsFor(small, 8, { ...NO_FIGURES, standardCpus: 1.5, standardMemoryMb: 4096 }),
    {
      slots: 2,
      held: 2,
      limitedBy: 'cpu',
      derived: true,
    },
  );
  // Memory is the smaller when the runners are memory-heavy.
  assert.deepEqual(slotsFor(big, 8, { ...NO_FIGURES, standardCpus: 1, standardMemoryMb: 16384 }), {
    slots: 1,
    held: 1,
    limitedBy: 'memory',
    derived: true,
  });
});

// 11.4 divided by 3.8 is 2.9999999999999996 in floating point, and a host that
// holds three runners must not be told it holds two.
test('a CPU share that does not divide evenly is not rounded down a runner', () => {
  const machine = { ...big, allocatableCpus: 3 * 3.8 };
  assert.equal(slotsFor(machine, 8, { ...NO_FIGURES, standardCpus: 3.8 }).slots, 3);
});

test('the capacity caps the derived count and is named as the limit when it is lower', () => {
  assert.deepEqual(slotsFor(big, 2, { ...NO_FIGURES, standardCpus: 3, standardMemoryMb: 8192 }), {
    slots: 2,
    held: 3,
    limitedBy: 'capacity',
    derived: true,
  });
  // Capacity equal to what the machine holds is nobody's limit.
  assert.deepEqual(slotsFor(big, 3, { ...NO_FIGURES, standardCpus: 3, standardMemoryMb: 8192 }), {
    slots: 3,
    held: 3,
    limitedBy: '',
    derived: true,
  });
});

test('a size never un-pauses a host, and a host with no size keeps its capacity', () => {
  assert.deepEqual(slotsFor(big, 0, { ...NO_FIGURES, standardCpus: 3 }), {
    slots: 0,
    held: 0,
    limitedBy: '',
    derived: false,
  });
  assert.deepEqual(slotsFor(big, 5, NO_FIGURES), {
    slots: 5,
    held: 5,
    limitedBy: '',
    derived: false,
  });
  // A minimum or a ceiling is not a size: neither gives the host a count.
  assert.deepEqual(slotsFor(big, 5, { ...NO_FIGURES, minCpus: 2, burstMaxCpus: 4 }), {
    slots: 5,
    held: 5,
    limitedBy: '',
    derived: false,
  });
});

test('a host that has not measured its machine has nothing to divide', () => {
  const unmeasured = machineOf({});
  assert.deepEqual(slotsFor(unmeasured, 4, { ...NO_FIGURES, standardCpus: 3 }), {
    slots: 4,
    held: 4,
    limitedBy: '',
    derived: false,
  });
  // Only the figures the machine has reported are divided: CPU is known and
  // memory is not, so memory does not bind.
  const cpusOnly = { ...big, memoryMb: 0, allocatableMemoryMb: 0 };
  assert.equal(
    slotsFor(cpusOnly, 8, { ...NO_FIGURES, standardCpus: 3, standardMemoryMb: 8192 }).slots,
    3,
  );
});

test('a standard larger than the machine gives no slots rather than a negative one', () => {
  assert.equal(slotsFor(small, 4, { ...NO_FIGURES, standardCpus: 8 }).slots, 0);
});

test('the controller counts slots, and the capacity stands in only where it has not said', () => {
  assert.equal(slotsOf({ capacity: 8, slots: 3 }), 3);
  assert.equal(slotsOf({ capacity: 8 }), 8);
  assert.equal(slotsOf({ capacity: 8, slots: 0 }), 0);
  assert.equal(slotsOf({}), 0);
});

test('a profile is held to the same floors as a pool', () => {
  const errors = profileErrors(
    { minCpus: 0.1, minMemoryMb: 100, standardCpus: 0.1, standardMemoryMb: 100, burstMaxCpus: 0.1 },
    machineOf({}),
  );
  assert.deepEqual(Object.keys(errors).sort(), [
    'runner_profile.minimum.cpus',
    'runner_profile.minimum.memory_mb',
    'runner_profile.standard.burst_max_cpus',
    'runner_profile.standard.cpus',
    'runner_profile.standard.memory_mb',
  ]);
  assert.deepEqual(profileErrors(NO_FIGURES, big), {});
  assert.match(
    profileErrors({ ...NO_FIGURES, burstMaxMemoryMb: 100 }, machineOf({}))[
      'runner_profile.standard.burst_max_memory_mb'
    ] ?? '',
    /at least 512 MB/,
  );
});

test('a minimum above the standard, and a ceiling below it, are refused where they are typed', () => {
  const errors = profileErrors(
    { minCpus: 4, minMemoryMb: 8192, standardCpus: 2, standardMemoryMb: 4096, burstMaxCpus: 1 },
    machineOf({}),
  );
  assert.match(errors['runner_profile.minimum.cpus'] ?? '', /above the standard, 2 cores/);
  assert.match(errors['runner_profile.minimum.memory_mb'] ?? '', /above the standard, 4 GB/);
  assert.match(errors['runner_profile.standard.burst_max_cpus'] ?? '', /at least the standard/);
  // Alone, neither has anything to be compared with.
  assert.deepEqual(profileErrors({ ...NO_FIGURES, minCpus: 4 }, machineOf({})), {});
  assert.deepEqual(profileErrors({ ...NO_FIGURES, burstMaxCpus: 1 }, machineOf({})), {});
});

test('a memory ceiling below the standard is refused: it would be a request to take memory back', () => {
  const errors = profileErrors(
    { ...NO_FIGURES, standardMemoryMb: 8192, burstMaxMemoryMb: 4096 },
    machineOf({}),
  );
  assert.match(
    errors['runner_profile.standard.burst_max_memory_mb'] ?? '',
    /at least the standard, 8 GB/,
  );
  assert.deepEqual(
    profileErrors({ ...NO_FIGURES, standardMemoryMb: 8192, burstMaxMemoryMb: 8192 }, machineOf({})),
    {},
    'at the standard is a ceiling with nothing to lend, which the server allows',
  );
  assert.deepEqual(profileErrors({ ...NO_FIGURES, burstMaxMemoryMb: 4096 }, machineOf({})), {});
});

test('a size the machine could never hold is refused only once the machine has said what it is', () => {
  const tooBig = { ...NO_FIGURES, standardCpus: 8, standardMemoryMb: 32768, minCpus: 6 };
  const errors = profileErrors(tooBig, small);
  assert.match(errors['runner_profile.standard.cpus'] ?? '', /could never run a runner here/);
  assert.match(errors['runner_profile.standard.memory_mb'] ?? '', /could never run a runner here/);
  assert.match(errors['runner_profile.minimum.cpus'] ?? '', /less than this minimum/);
  // The same figures on a host that has not reported its machine are saved:
  // there is nothing to compare them with, and the host is placed by slots.
  assert.deepEqual(profileErrors(tooBig, machineOf({})), {});
});
