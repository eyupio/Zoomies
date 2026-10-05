import { test } from 'node:test';
import assert from 'node:assert/strict';
import type { MemoryResourceState, RunnerScratch } from '../src/lib/api/types.ts';
import {
  blockedWords,
  memoryBadges,
  sentenceCase,
  times,
} from '../src/lib/runners/memory-badges.ts';

/** A valve that has done nothing, which each case then changes in the one way it is about. */
function valve(over: Partial<MemoryResourceState> = {}): MemoryResourceState {
  return {
    state: 'watching',
    mode: 'automatic',
    guaranteed_mb: 5120,
    current_mb: 5120,
    ceiling_mb: 7680,
    lent_mb: 0,
    ...over,
  };
}

const lentTwice = valve({
  state: 'lent',
  current_mb: 6656,
  lent_mb: 1536,
  raises: 2,
  reason: 'raised the limit from 6144 to 6656 MB: it was using 5400 MB',
});

test('a runner the valve has nothing to say about has no badge', () => {
  assert.deepEqual(memoryBadges({}), []);
});

test('a valve that has done nothing is quiet in the grid and says it is watching on the runner page', () => {
  const runner = { memory_resource: valve() };
  assert.deepEqual(memoryBadges(runner), []);
  const [watching] = memoryBadges(runner, { quiet: true });
  assert.equal(watching?.key, 'watching');
  assert.equal(watching?.tone, 'neutral');
  assert.match(watching?.detail ?? '', /stayed well inside its limit/);
});

test('lent memory is a pill with the figure on it and a card with the three sizes and a bar', () => {
  const [badge, ...rest] = memoryBadges({ memory_resource: lentTwice });
  assert.equal(rest.length, 0);
  assert.equal(badge?.key, 'memory');
  assert.equal(badge?.tone, 'accent');
  assert.equal(badge?.label, '+1.5 GB');
  assert.deepEqual(
    badge?.figures.map((f) => [f.label, f.value]),
    [
      ['Guaranteed', '5.0 GB'],
      ['Current', '6.5 GB'],
      ['Ceiling', '7.5 GB'],
    ],
  );
  assert.deepEqual(badge?.bar, { guaranteed: 5120, lent: 1536, swap: 0, ceiling: 7680 });
  assert.match(badge?.detail ?? '', /raised twice/);
  assert.match(badge?.detail ?? '', /never taken back/);
  // The agent's own sentence, with its numbers, is the reason on the card.
  assert.deepEqual(badge?.notes, ['Raised the limit from 6144 to 6656 MB: it was using 5400 MB.']);
  assert.equal(badge?.wanting, false);
  assert.equal(badge?.dashed, false);
});

test('a runner that holds a loan and wants more says so, with a mark on the pill', () => {
  const [badge] = memoryBadges({
    memory_resource: valve({
      state: 'lent',
      current_mb: 7680,
      lent_mb: 2560,
      raises: 3,
      blocked: 'at_ceiling',
      near_limit: true,
    }),
  });
  assert.equal(badge?.wanting, true);
  // With no sentence from the agent the general words stand in for it.
  assert.ok(
    badge?.notes.includes(blockedWords(valve({ blocked: 'at_ceiling', current_mb: 7680 }))),
  );
  assert.ok(badge?.notes.some((n) => /within a tenth/.test(n)));
});

test('swap is its own pill, beside the loan it follows', () => {
  const badges = memoryBadges({
    memory_resource: valve({
      state: 'spilled',
      current_mb: 7680,
      lent_mb: 2560,
      spill_mb: 2048,
      spill_allowed_mb: 2048,
      raises: 3,
    }),
  });
  assert.deepEqual(
    badges.map((b) => [b.key, b.tone]),
    [
      ['memory', 'accent'],
      ['swap', 'pending'],
    ],
  );
  const swap = badges[1];
  assert.equal(swap?.label, 'Swap');
  assert.match(swap?.title ?? '', /2\.0 GB of swap/);
  // The three sizes are on the loan's card, not said twice.
  assert.deepEqual(swap?.figures, []);
  assert.equal(swap?.bar, undefined);
  assert.ok(swap?.notes.some((n) => /up to 2\.0 GB of swap/.test(n)));
  assert.deepEqual(badges[0]?.bar?.swap, 2048);
});

test('swap with no loan carries the bar and the sizes itself', () => {
  const [swap, ...rest] = memoryBadges({
    memory_resource: valve({ state: 'spilled', spill_mb: 1024 }),
  });
  assert.equal(rest.length, 0);
  assert.equal(swap?.key, 'swap');
  assert.equal(swap?.figures.length, 4);
  assert.deepEqual(swap?.bar, { guaranteed: 5120, lent: 0, swap: 1024, ceiling: 7680 });
});

test('an observing valve is a dashed pill saying what it would have done, never what it did', () => {
  const [badge] = memoryBadges({
    memory_resource: valve({
      state: 'observing',
      mode: 'observe',
      would_lend_mb: 1024,
      near_limit: true,
      reason: 'would have raised the limit from 8192 to 9216 MB: it was using 7400 MB',
    }),
  });
  assert.equal(badge?.key, 'observing');
  assert.equal(badge?.dashed, true);
  assert.equal(badge?.tone, 'neutral');
  assert.equal(badge?.label, '~1.0 GB');
  assert.match(badge?.title ?? '', /Would have lent 1\.0 GB/);
  assert.match(badge?.detail ?? '', /changes nothing/);
  assert.deepEqual(
    badge?.figures.map((f) => f.label),
    ['Guaranteed', 'Would hold', 'Ceiling'],
  );
  assert.equal(badge?.figures[1]?.value, '6.0 GB');
  // The agent words an observed decision as what it would have done, and the card
  // carries that sentence as it came.
  assert.deepEqual(badge?.notes.slice(0, 1), [
    'Would have raised the limit from 8192 to 9216 MB: it was using 7400 MB.',
  ]);
});

test('an observing valve that would not have lent anything is quiet unless the runner came near its limit', () => {
  const quiet = { memory_resource: valve({ state: 'observing', mode: 'observe' }) };
  assert.deepEqual(memoryBadges(quiet), []);
  const near = memoryBadges({
    memory_resource: valve({ state: 'observing', mode: 'observe', near_limit: true }),
  });
  assert.equal(near[0]?.label, 'Near limit');
});

test('a runner that wanted memory and was refused it is told apart by who refused', () => {
  const cases: [NonNullable<MemoryResourceState['blocked']>, string, string, boolean][] = [
    ['at_ceiling', 'At ceiling', 'pending', true],
    ['pool_empty', 'Host full', 'pending', true],
    ['host_floor', 'Host full', 'pending', true],
    ['unmeasured', 'Unmeasured', 'neutral', true],
    ['unsupported', 'Unsupported', 'neutral', false],
    ['failed', 'Raise failed', 'danger', true],
  ];
  for (const [blocked, label, tone, wanting] of cases) {
    const [badge, ...rest] = memoryBadges({ memory_resource: valve({ blocked }) });
    assert.equal(rest.length, 0, blocked);
    assert.equal(badge?.key, 'capped', blocked);
    assert.equal(badge?.label, label, blocked);
    assert.equal(badge?.tone, tone, blocked);
    assert.equal(badge?.wanting, wanting, blocked);
    assert.ok((badge?.notes[0] ?? '') !== '', `${blocked} says why`);
  }
});

test('a loan that stands after the pool switches the valve off is still shown, and says the valve is off', () => {
  const [badge] = memoryBadges({ memory_resource: { ...lentTwice, mode: 'off' } });
  assert.equal(badge?.key, 'memory');
  assert.match(badge?.eyebrow ?? '', /now off for this pool/);
});

const scratch = (folders: RunnerScratch['folders']): RunnerScratch => ({
  in_memory: folders.filter((f) => f.in_memory).length,
  folders,
});

const work = {
  kind: 'work' as const,
  label: 'Work folder',
  path: '/home/runner/_work',
  in_memory: true,
  size_mb: 2048,
  asked_mb: 4096,
  auto: true,
};
const tmp = {
  kind: 'tmp' as const,
  label: 'Temporary folder',
  path: '/tmp',
  in_memory: true,
  size_mb: 1024,
  asked_mb: 1024,
};
const image = {
  kind: 'daemon' as const,
  label: 'Docker image store',
  path: '/var/lib/docker',
  in_memory: false,
  asked_mb: 4096,
  auto: true,
  why: 'auto_too_small' as const,
  note: 'Kept on disk: this runner’s memory limit left the docker image store less than the 4.0 GB it is worth having in memory.',
};

test('folders in memory are one pill with an icon for each and what they come to', () => {
  const [badge] = memoryBadges({ scratch: scratch([work, tmp]) });
  assert.equal(badge?.key, 'folders');
  assert.deepEqual(badge?.icons, ['folder', 'tmp']);
  assert.equal(badge?.label, '3.0 GB');
  assert.equal(badge?.dashed, false);
  assert.equal(badge?.title, 'All 2 folders are in memory');
  const [first] = badge?.folders ?? [];
  assert.deepEqual(first?.tags, ['Auto', 'fitted to its limit from 4.0 GB']);
  assert.equal(first?.size, '2.0 GB');
});

test('a folder that stayed on disk says why, in the card, and the pill counts only what is in memory', () => {
  const [badge] = memoryBadges({ scratch: scratch([work, tmp, image]) });
  assert.equal(badge?.title, '2 of 3 folders in memory');
  assert.deepEqual(badge?.icons, ['folder', 'tmp']);
  const disk = badge?.folders.find((f) => !f.inMemory);
  assert.equal(disk?.size, 'On disk');
  assert.equal(disk?.icon, 'image');
  assert.match(disk?.note ?? '', /Kept on disk/);
  assert.match(
    badge?.text ?? '',
    /Docker image store at \/var\/lib\/docker: on disk\. Kept on disk/,
  );
});

test('a pool that asked for memory and gave this runner none is a dashed pill, not a missing one', () => {
  const [badge] = memoryBadges({
    scratch: scratch([
      {
        ...work,
        in_memory: false,
        size_mb: undefined,
        why: 'host_off',
        note: 'Kept on disk: this host’s owner has turned in-memory folders off.',
      },
    ]),
  });
  assert.equal(badge?.dashed, true);
  assert.equal(badge?.label, 'On disk');
  assert.deepEqual(badge?.icons, ['disk']);
  assert.match(badge?.title ?? '', /Kept on disk, though its pool asked for memory/);
});

test('a single folder in memory is named, not counted', () => {
  const [badge] = memoryBadges({ scratch: scratch([{ ...tmp }]) });
  assert.equal(badge?.title, 'Temporary folder is in memory');
});

test('what the valve has done comes before the folders, because it changes and they do not', () => {
  const badges = memoryBadges({ memory_resource: lentTwice, scratch: scratch([work]) });
  assert.deepEqual(
    badges.map((b) => b.key),
    ['memory', 'folders'],
  );
});

test('every card is also one sentence, which is what a screen reader hears', () => {
  for (const badge of memoryBadges(
    {
      memory_resource: { ...lentTwice, spill_mb: 512, spill_allowed_mb: 512 },
      scratch: scratch([work, image]),
    },
    { quiet: true },
  )) {
    assert.ok(badge.text.length > 40, badge.key);
    assert.ok(badge.text.includes(badge.title), badge.key);
    assert.doesNotMatch(badge.text, /\s{2,}/, badge.key);
  }
});

test('the small words', () => {
  assert.equal(times(1), 'once');
  assert.equal(times(2), 'twice');
  assert.equal(times(5), '5 times');
  assert.equal(sentenceCase('raised the limit'), 'Raised the limit.');
  assert.equal(sentenceCase('already ends.'), 'Already ends.');
  assert.equal(sentenceCase('  '), '');
});
