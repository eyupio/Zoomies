import { test } from 'node:test';
import assert from 'node:assert/strict';
import { toolLabel } from '../src/lib/assistant/tools.ts';

test('a tool is called by what it shows, and one nobody named is its own words', () => {
  assert.equal(toolLabel('list_runners'), 'runners');
  assert.equal(toolLabel('get_runner_log'), 'a runner’s log');
  assert.equal(toolLabel('some_new_tool'), 'some new tool');
});
