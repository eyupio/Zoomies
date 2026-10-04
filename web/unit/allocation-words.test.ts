import { test } from 'node:test';
import assert from 'node:assert/strict';
import { allocationSourceWords, allocationWords } from '../src/lib/runners/allocation.ts';

// The runner's page and the job's drawer say this about the same figure, and an
// existing page test pins the first sentence word for word.
test('what a runner was given is said with the figure and where it came from', () => {
  assert.equal(allocationWords(1.87, 3994, 'host'), "1.87 CPU · 3.9 GB, the host's default share");
  assert.equal(
    allocationWords(3, 8192, 'profile'),
    '3 CPU · 8.0 GB, the standard size set for its host',
  );
  assert.equal(allocationWords(2, 4096, 'pool'), '2 CPU · 4.0 GB, from the pool');
  assert.match(allocationWords(1, 2048, 'reduced'), /^1 CPU · 2.0 GB, reduced: no host had room/);
  assert.match(allocationWords(2, 4096, 'history'), /sized for what the jobs waiting/);
});

test('a figure with no source is still a figure, and no figure is nothing', () => {
  assert.equal(allocationWords(2, 4096, undefined), '2 CPU · 4.0 GB');
  assert.equal(allocationWords(2, 4096, 'a-source-from-a-later-release'), '2 CPU · 4.0 GB');
  assert.equal(allocationWords(0, 4096, 'pool'), '4.0 GB, from the pool');
  assert.equal(allocationWords(undefined, undefined, 'host'), '');
  assert.equal(allocationSourceWords(undefined), '');
});
