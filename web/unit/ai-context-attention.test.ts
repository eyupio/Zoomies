import { test } from 'node:test';
import assert from 'node:assert/strict';
import { aiContextAttention } from '../src/lib/aicontext/attention.ts';

const run = (severity: 'error' | 'warning' | 'info', kind = 'ai_context') => ({
  target_kind: kind,
  severity,
});

test('a fleet with no failed AI Context run has nothing to show', () => {
  assert.equal(aiContextAttention([]), null);
});

// The problems list is everything wrong with the fleet. A pool that cannot place
// a runner, or a repository Kennel Club is worried about, is not a failed AI
// Context run, and a badge that counted them would say AI Context is failing when
// it is not.
test('only failed AI Context runs count, whatever else is wrong', () => {
  assert.equal(aiContextAttention([run('error', 'pool'), run('error', 'kennel')]), null);
  const found = aiContextAttention([run('error', 'pool'), run('warning'), run('error', 'kennel')]);
  assert.equal(found?.count, 1);
});

test('the badge is drawn as the worst run that failed', () => {
  assert.equal(aiContextAttention([run('warning'), run('warning')])?.worst, 'warning');
  assert.equal(aiContextAttention([run('warning'), run('error')])?.worst, 'error');
  assert.equal(aiContextAttention([run('error'), run('warning')])?.worst, 'error');
});

test('it says how many repositories in words, one or several', () => {
  assert.equal(aiContextAttention([run('error')])?.text, '1 repository needs attention');
  assert.equal(
    aiContextAttention([run('error'), run('warning'), run('warning')])?.text,
    '3 repositories need attention',
  );
  assert.equal(aiContextAttention([run('warning'), run('error')])?.count, 2);
});
