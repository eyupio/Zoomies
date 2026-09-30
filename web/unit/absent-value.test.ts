import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';
import * as format from '../src/lib/format.ts';

const lib = join(import.meta.dirname, '..', 'src');

// Three glyphs for "nothing to show" meant a table cell and the tile above it
// disagreed about what an empty value looks like. The formatters already
// answered with one; the constant is how a page that has no formatter to call
// says the same thing.
test('the formatters and the pages agree on one marker for a missing value', () => {
  assert.equal(format.NO_VALUE, '--');
  for (const missing of [
    format.formatAbsolute(null),
    format.formatDuration(null),
    format.formatNumber(null),
  ]) {
    assert.equal(missing, format.NO_VALUE);
  }
});

const PAGES = [
  'lib/usage/UsageInsights.svelte',
  'lib/insights/JobsInsights.svelte',
  'lib/insights/RunnerInsights.svelte',
  'lib/insights/PoolPressure.svelte',
  'lib/insights/FigureRows.svelte',
  'lib/insights/ReadingCard.svelte',
  'lib/insights/HostCapacityMap.svelte',
  'routes/Queue.svelte',
  'routes/Hosts.svelte',
];

for (const page of PAGES) {
  test(`${page} writes a missing value with the shared marker, not a dash of its own`, () => {
    const source = readFileSync(join(lib, page), 'utf8');
    // A dash standing alone as a value: a string literal of one, or one
    // between tags. Dashes inside a sentence and number ranges are prose.
    assert.doesNotMatch(source, /(['"`])[—–]\1|>[—–]</);
  });
}
