import { test } from 'node:test';
import assert from 'node:assert/strict';
import { runsOn, testWorkflow } from '../src/lib/brand.ts';

// The sample is pasted into somebody else's repository, so the one thing it
// must never do is send the job to a different pool from the one the checklist
// above it just named. It asks `runsOn` rather than guessing at a label.
test('the test workflow runs on exactly what the checklist tells the operator to write', () => {
  for (const labels of [['zoomies'], ['zoomies', 'zoomies-gpu', 'cuda'], []]) {
    assert.match(testWorkflow(labels), new RegExp(`^ {4}runs-on: ${escape(runsOn(labels))}$`, 'm'));
  }
});

// Nothing to push: the whole point of the sample is that an evaluator with no
// workflow worth editing can start a job from the Actions tab.
test('the test workflow is started by hand and needs no checkout, language or Makefile', () => {
  const yaml = testWorkflow(['zoomies']);
  assert.match(yaml, /^on: workflow_dispatch$/m);
  assert.doesNotMatch(yaml, /actions\/checkout|make |npm |setup-/);
  assert.ok(yaml.endsWith('\n'), 'a file ends in a newline');
});

function escape(text: string): string {
  return text.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}
