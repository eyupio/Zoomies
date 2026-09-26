import { test } from 'node:test';
import assert from 'node:assert/strict';
import { STATE_PRESENTATION, bandWords, sinceWords, waitWords } from '../src/status/present.ts';

// The three states sit on the fixed status mapping, one tone and one shape
// each, so the status page reads the way every other page does and never
// leans on colour alone.
test('each fleet state has its own tone and its own shape', () => {
  assert.equal(STATE_PRESENTATION.blocked.tone, 'danger');
  assert.equal(STATE_PRESENTATION.degraded.tone, 'pending');
  assert.equal(STATE_PRESENTATION.healthy.tone, 'idle');
  const shapes = new Set(Object.values(STATE_PRESENTATION).map((p) => p.shape));
  assert.equal(shapes.size, 3);
});

test('bands and waits are said in words, never as the raw value', () => {
  assert.equal(bandWords('backed_up'), 'backed up');
  assert.equal(bandWords('few'), 'a few');
  assert.equal(waitWords(0), 'under a minute');
  assert.equal(waitWords(1), 'about a minute');
  assert.equal(waitWords(14), 'about 14 minutes');
  assert.equal(waitWords(125), 'about 2 hours');
});

test('since is coarse, and empty when there is nothing to say', () => {
  const now = new Date('2026-09-26T12:00:00Z');
  assert.equal(sinceWords(undefined, now), '');
  assert.equal(sinceWords('not a date', now), '');
  assert.equal(sinceWords('2026-09-26T11:59:40Z', now), 'just now');
  assert.equal(sinceWords('2026-09-26T11:15:00Z', now), '45 minutes ago');
  assert.equal(sinceWords('2026-09-26T09:00:00Z', now), '3 hours ago');
  assert.equal(sinceWords('2026-09-22T12:00:00Z', now), '4 days ago');
});
