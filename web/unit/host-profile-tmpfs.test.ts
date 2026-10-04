import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  NO_FIGURES,
  figuresOf,
  isUnset,
  profileBody,
  profileErrors,
  sameFigures,
} from '../src/lib/hosts/profile.ts';

const machine = { cpus: 8, memoryMb: 32768, allocatableCpus: 7, allocatableMemoryMb: 28672 };

// The policy is part of the profile the API replaces whole, so it has to go out
// with the figures beside it and come back the same.
test("a host's in-memory policy round-trips with its runner profile", () => {
  const profile = {
    standard: { cpus: 2, memory_mb: 8192 },
    tmpfs: { disabled: true },
  };
  const figures = figuresOf(profile);
  assert.equal(figures.tmpfsOff, true);
  assert.equal(figures.standardCpus, 2);
  assert.deepEqual(profileBody(figures), profile);
  assert.deepEqual(profileBody(figuresOf({ tmpfs: { max_mb: 2048 } })), {
    tmpfs: { max_mb: 2048 },
  });
});

// A host that says nothing sends nothing, so it keeps following each pool.
test('a host that says nothing about in-memory folders sends nothing about them', () => {
  assert.deepEqual(profileBody(NO_FIGURES), {});
  assert.equal(isUnset(NO_FIGURES), true);
  assert.equal(isUnset({ ...NO_FIGURES, tmpfsOff: true }), false);
  assert.equal(isUnset({ ...NO_FIGURES, tmpfsMaxMb: 512 }), false);
  assert.equal(sameFigures(NO_FIGURES, { ...NO_FIGURES, tmpfsMaxMb: 512 }), false);
});

// Off is stronger than a ceiling: the API refuses both together, so the form
// never sends the ceiling of a host that keeps the folders off.
test('a ceiling is not sent for a host that keeps the folders off', () => {
  const body = profileBody({ ...NO_FIGURES, tmpfsOff: true, tmpfsMaxMb: 2048 });
  assert.deepEqual(body, { tmpfs: { disabled: true } });
});

test('a ceiling below the floor is refused where it is typed, and only while it is in force', () => {
  const small = profileErrors({ ...NO_FIGURES, tmpfsMaxMb: 32 }, machine);
  assert.match(small['runner_profile.tmpfs.max_mb'] ?? '', /below 64 MB/);
  assert.deepEqual(profileErrors({ ...NO_FIGURES, tmpfsMaxMb: 64 }, machine), {});
  // Moot on a host that keeps the folders off.
  assert.deepEqual(profileErrors({ ...NO_FIGURES, tmpfsOff: true, tmpfsMaxMb: 32 }, machine), {});
});

// A host's standard folder sizes are the folder-sized counterpart of its standard
// runner size: part of the profile the API replaces whole, so they round-trip
// with the rest, are not sent for a host that falls back to disk, and are held to
// the same floor as a ceiling.
test("a host's standard folder sizes round-trip and are not sent where the folders are off", () => {
  const profile = { tmpfs: { work_mb: 16384, tmp_mb: 2048, daemon_mb: 32768, max_mb: 20000 } };
  const figures = figuresOf(profile);
  assert.equal(figures.tmpfsWorkMb, 16384);
  assert.equal(figures.tmpfsDaemonMb, 32768);
  assert.deepEqual(profileBody(figures), profile);
  assert.deepEqual(profileBody({ ...figures, tmpfsOff: true }), { tmpfs: { disabled: true } });
  assert.equal(isUnset({ ...NO_FIGURES, tmpfsWorkMb: 4096 }), false);
  assert.equal(sameFigures(NO_FIGURES, { ...NO_FIGURES, tmpfsTmpMb: 512 }), false);
});

test('a standard folder size below the floor is refused where it is typed', () => {
  const errors = profileErrors({ ...NO_FIGURES, tmpfsWorkMb: 10, tmpfsDaemonMb: 20 }, machine);
  assert.match(errors['runner_profile.tmpfs.work_mb'] ?? '', /below 64 MB/);
  assert.match(errors['runner_profile.tmpfs.daemon_mb'] ?? '', /below 64 MB/);
  assert.equal(errors['runner_profile.tmpfs.tmp_mb'], undefined);
  assert.deepEqual(profileErrors({ ...NO_FIGURES, tmpfsTmpMb: 64 }, machine), {});
});
