import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  CACHE_NOTCHES,
  CPU_NOTCHES,
  DEFAULT_TMPFS_DAEMON_MB,
  DEFAULT_TMPFS_TMP_MB,
  DEFAULT_TMPFS_WORK_MB,
  MEMORY_NOTCHES,
  cacheBytes,
  cacheGb,
  chargedSize,
  cpuLabel,
  gbLabel,
  memoryLabel,
  daemonReserveMb,
  recommendedMemoryMb,
  tmpfsIsTight,
  tmpfsReserveMb,
  nearest,
  roomOn,
  sizeLabel,
  withValue,
} from '../src/lib/pools/sizing.ts';

// The floors the API enforces have to be on the sliders, or the control offers
// a value the server refuses and the wizard argues with itself.
test('the notches start at what a runner needs to be a runner', () => {
  assert.equal(CPU_NOTCHES[0], 0.25);
  assert.equal(MEMORY_NOTCHES[0], 512);
  assert.equal(CACHE_NOTCHES[0], 0);
});

test('a value the notches do not have joins them rather than snapping', () => {
  // A pool created through the API can hold 3000 MB. Rounding it to 3072 the
  // moment its page opens would change a pool somebody came to read.
  assert.deepEqual(withValue([1024, 2048, 4096], 3000), [1024, 2048, 3000, 4096]);
  assert.deepEqual(withValue([1024, 2048], 2048), [1024, 2048]);
  assert.deepEqual(withValue([1024, 2048], 0), [1024, 2048]);
});

test('the nearest notch is where a figure typed elsewhere lands', () => {
  assert.equal(nearest(MEMORY_NOTCHES, 3800), 4096);
  assert.equal(nearest(CPU_NOTCHES, 2.4), 2);
});

test('a cache limit round-trips between the slider and the bytes the API takes', () => {
  assert.equal(cacheBytes(10), 10 * 1024 * 1024 * 1024);
  assert.equal(cacheGb(10 * 1024 * 1024 * 1024), 10);
  assert.equal(cacheGb(0), 0);
  assert.equal(cacheGb(undefined), 0);
  // A limit typed off the notches is kept as typed, not moved to the nearest.
  assert.equal(cacheGb(7 * 1024 * 1024 * 1024), 7);
});

test('the words say what the figure means rather than repeating it', () => {
  assert.equal(cpuLabel(0.5), 'half a core');
  assert.equal(cpuLabel(1), '1 core');
  assert.equal(cpuLabel(2), '2 cores');
  assert.equal(memoryLabel(512), '512 MB');
  assert.equal(memoryLabel(4096), '4 GB');
  assert.equal(memoryLabel(0), 'none');
  assert.equal(gbLabel(0), 'no limit');
  assert.equal(sizeLabel(2, 4096), '2 cores and 4 GB');
});

// The sidecar gets the same limits as the runner, so a docker-in-docker pool
// asking for two cores puts four on the machine. It is the figure nothing else
// on the form shows.
test('a pool that gives its jobs a daemon is charged for the pair', () => {
  assert.deepEqual(chargedSize(2, 4096, 'dind'), { cpus: 4, memoryMb: 8192, pair: true });
  assert.deepEqual(chargedSize(2, 4096, 'none'), { cpus: 2, memoryMb: 4096, pair: false });
});

test('the room is what both figures allow, and never negative', () => {
  assert.equal(roomOn({ cpus: 9.5, memoryMb: 19968 }, { cpus: 2, memoryMb: 4096 }), 4);
  // Memory is the tighter of the two here, and the answer follows it.
  assert.equal(roomOn({ cpus: 16, memoryMb: 8192 }, { cpus: 1, memoryMb: 4096 }), 2);
  assert.equal(roomOn({ cpus: 2, memoryMb: 2048 }, { cpus: 8, memoryMb: 16384 }), 0);
  // A machine that has measured nothing constrains nothing, and the caller
  // falls back to the host's slots.
  assert.equal(roomOn({ cpus: 0, memoryMb: 0 }, { cpus: 2, memoryMb: 4096 }), 0);
});

// The same arithmetic as store.TmpfsConfig.ReserveMB and RecommendedMemoryMB:
// the editor's proposal and the controller's warning must name one number.
test('the in-memory folders reserve what was typed, or the default for one left to size itself', () => {
  const off = { enabled: false, sizeMb: undefined };
  assert.equal(tmpfsReserveMb(off, off), 0);
  assert.equal(tmpfsReserveMb({ enabled: true, sizeMb: undefined }, off), DEFAULT_TMPFS_WORK_MB);
  assert.equal(
    tmpfsReserveMb({ enabled: true, sizeMb: 6000 }, { enabled: true, sizeMb: undefined }),
    6000 + DEFAULT_TMPFS_TMP_MB,
  );
  // A size on a folder that is off is the form remembering, not a cost.
  assert.equal(tmpfsReserveMb({ enabled: false, sizeMb: 9999 }, off), 0);
});

test('the proposed memory limit is the current one plus what the folders may fill', () => {
  const work = { enabled: true, sizeMb: undefined };
  const off = { enabled: false, sizeMb: undefined };
  assert.equal(recommendedMemoryMb(8192, work, off), 8192 + DEFAULT_TMPFS_WORK_MB);
  // Nothing to raise where the host picks the size, or where nothing is in memory.
  assert.equal(recommendedMemoryMb(0, work, off), 0);
  assert.equal(recommendedMemoryMb(8192, off, off), 0);
});

// The proposal must end when it is taken. At the line the folders are fitted
// into half the limit and are no problem; past it they are shrunk or, if typed,
// a way to be killed for memory -- the same line pool.tmpfs_memory_tight draws.
test('the folders are only worth a word when they take more than half the limit', () => {
  assert.equal(tmpfsIsTight(8192, 4096), false);
  assert.equal(tmpfsIsTight(8192, 4097), true);
  assert.equal(tmpfsIsTight(6144, 4096), true);
  // Accepting the proposal ends it.
  assert.equal(tmpfsIsTight(6144 + 4096, 4096), false);
  // No limit typed means the host picks the size, so there is nothing to raise.
  assert.equal(tmpfsIsTight(0, 4096), false);
});

// The same arithmetic as store.TmpfsConfig.RecommendedMemoryMB: a typed limit is
// given to the runner and to the daemon alike, each charged for its own folders,
// so the proposal covers whichever needs more rather than both.
test('the proposed limit covers the container that needs more, not both', () => {
  const off = { enabled: false, sizeMb: undefined };
  const work = { enabled: true, sizeMb: 2000 };
  assert.equal(daemonReserveMb(off), 0);
  assert.equal(daemonReserveMb({ enabled: true, sizeMb: undefined }), DEFAULT_TMPFS_DAEMON_MB);
  assert.equal(recommendedMemoryMb(8192, work, off, { enabled: true, sizeMb: 6000 }), 8192 + 6000);
  assert.equal(recommendedMemoryMb(8192, work, off, { enabled: true, sizeMb: 1000 }), 8192 + 2000);
  // A pool with only its image store in memory is still proposed a limit.
  assert.equal(
    recommendedMemoryMb(8192, off, off, { enabled: true, sizeMb: undefined }),
    8192 + DEFAULT_TMPFS_DAEMON_MB,
  );
});

// A proposal that is taken must end itself: the limit is at least twice what the
// folders may take, which is more than the limit plus them on a small limit.
test('a proposed limit leaves the folders no more than half of it', () => {
  const off = { enabled: false, sizeMb: undefined };
  const work = { enabled: true, sizeMb: undefined };
  const daemon = { enabled: true, sizeMb: undefined };
  for (const limit of [1024, 2048, 6144, 8192]) {
    const proposed = recommendedMemoryMb(limit, work, off, daemon);
    assert.ok(proposed >= 2 * DEFAULT_TMPFS_DAEMON_MB, `${limit} -> ${proposed}`);
    assert.equal(tmpfsIsTight(proposed, DEFAULT_TMPFS_DAEMON_MB), false);
  }
  assert.equal(recommendedMemoryMb(6144, work, off), 6144 + DEFAULT_TMPFS_WORK_MB);
});
