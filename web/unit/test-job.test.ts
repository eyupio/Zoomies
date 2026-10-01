import { test } from 'node:test';
import assert from 'node:assert/strict';
import { TEST_JOB_FILE, TEST_JOB_NAME, testJobWorkflow } from '../src/lib/pools/test-job.ts';

// The whole point of the probe is that it is a complete file. The Quick start's
// snippet used to start at `jobs:`, which GitHub refuses as a workflow -- no
// trigger -- so an evaluator with a fresh organisation met an error at the last
// step before value. This pins the parts GitHub insists on.
test('the test job is a whole workflow file, not a fragment', () => {
  const yaml = testJobWorkflow(['zoomies', 'zoomies-linux-x64']);
  const lines = yaml.split('\n');
  assert.equal(lines[0], `name: ${TEST_JOB_NAME}`);
  assert.ok(lines.includes('on: workflow_dispatch'), 'it has a trigger');
  assert.ok(lines.includes('jobs:'), 'it has jobs');
  assert.ok(yaml.endsWith('\n'), 'it ends with a newline, as a file does');
  assert.ok(
    yaml.indexOf('on: workflow_dispatch') < yaml.indexOf('jobs:'),
    'the trigger comes before the jobs it starts',
  );
  assert.ok(TEST_JOB_FILE.startsWith('.github/workflows/') && TEST_JOB_FILE.endsWith('.yml'));
});

// A job that asks for the wrong pool never runs, and the operator concludes the
// product does not work. It asks for exactly what the rest of the product prints.
test('the test job asks for the pool the way every other surface does', () => {
  assert.match(
    testJobWorkflow(['zoomies', 'zoomies-linux-x64']),
    /^ {4}runs-on: zoomies-linux-x64$/m,
  );
  // A pool that needs two labels to be identified gets the list form, because
  // dropping one would send the job to a different pool.
  assert.match(
    testJobWorkflow(['zoomies', 'cuda', 'zoomies-gpu']),
    /^ {4}runs-on: \[cuda, zoomies-gpu\]$/m,
  );
  // No labels at all still reaches the fleet.
  assert.match(testJobWorkflow([]), /^ {4}runs-on: self-hosted$/m);
});

// It must outlive the runner's own start-up, or a runner appears and vanishes
// before anybody has looked from GitHub to Zoomies.
test('the test job stays busy long enough to be watched', () => {
  assert.match(testJobWorkflow(['zoomies']), /sleep \d+/);
});
