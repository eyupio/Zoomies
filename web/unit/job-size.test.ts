import { test } from 'node:test';
import assert from 'node:assert/strict';
import { adviceWords, classCell, jobSize, pinKey, pinScope } from '../src/lib/jobs/size.ts';
import type { Job } from '../src/lib/api/types.ts';

const job = (patch: Partial<Job>): Job => ({ id: 'job_1', ...patch }) as Job;

test('a job nobody classed has no size to show, so the drawer is the one it always was', () => {
  assert.equal(jobSize(job({})), null);
  assert.equal(classCell(job({})), '');
});

test('a classed job says which class, on what authority, and the controller’s reason', () => {
  const view = jobSize(
    job({
      size_class: 'large',
      size_basis: 'history',
      size_reason: 'its last 12 runs used 6 GB at the 90th percentile, which a large runner holds',
    }),
  );
  assert.equal(view?.word, 'Large');
  assert.equal(view?.basisWords, 'From its earlier runs');
  assert.equal(
    view?.because,
    'Classed large because its last 12 runs used 6 GB at the 90th percentile, which a large runner holds.',
  );
  assert.equal(view?.fallback, null);
  assert.equal(view?.ran, null);
});

test('a job sent to another class is a fallback, and carries the controller’s note', () => {
  const view = jobSize(
    job({
      size_class: 'medium',
      routed_class: 'large',
      routed_note: 'No medium host has room.',
      ran_class: 'large',
    }),
  );
  assert.deepEqual(view?.fallback, { to: 'large', note: 'No medium host has room.' });
  assert.equal(view?.ran?.asExpected, true);
  assert.equal(view?.ran?.text, 'Ran on a large host, the class it was sent to instead of medium.');
});

test('a job that ran on the class it was put in says so plainly', () => {
  const view = jobSize(job({ size_class: 'small', ran_class: 'small' }));
  assert.equal(view?.ran?.asExpected, true);
  assert.equal(view?.ran?.text, 'Ran on a small host, the class it was put in.');
});

test('a job that landed elsewhere is not called a fault, and is told how to make it a promise', () => {
  const view = jobSize(job({ size_class: 'medium', ran_class: 'large' }));
  assert.equal(view?.ran?.asExpected, false);
  assert.match(view?.ran?.text ?? '', /Ran on a large host, not the medium one it was put in/);
  assert.match(view?.ran?.text ?? '', /matched to a runner by GitHub/);
  assert.match(view?.ran?.text ?? '', /Write the class label in its runs-on/);
  assert.doesNotMatch(view?.ran?.text ?? '', /fault|failed|error/i);
});

test('the expected class for a job that fell back is the one it was sent to', () => {
  // It was sent to large, and a small runner took it: that is the miss, and the
  // sentence names the class it was sent to rather than the one it began in.
  const view = jobSize(job({ size_class: 'medium', routed_class: 'large', ran_class: 'small' }));
  assert.match(view?.ran?.text ?? '', /not the large one it was sent to/);
});

test('the share of its CPU periods a job was held back in is a whole percentage', () => {
  assert.equal(jobSize(job({ size_class: 'small', throttled_share: 0.426 }))?.throttledPercent, 43);
  assert.equal(jobSize(job({ size_class: 'small', throttled_share: 0 }))?.throttledPercent, 0);
  // Never sampled is not the same as never throttled.
  assert.equal(
    jobSize(job({ size_class: 'small', throttled_share: null }))?.throttledPercent,
    null,
  );
  assert.equal(jobSize(job({ size_class: 'small' }))?.throttledPercent, null);
});

test('the grid cell shows a move only when the job ran somewhere other than its class', () => {
  assert.equal(classCell(job({ size_class: 'medium' })), 'Medium');
  assert.equal(classCell(job({ size_class: 'medium', ran_class: 'medium' })), 'Medium');
  assert.equal(classCell(job({ size_class: 'medium', ran_class: 'large' })), 'Medium → Large');
});

test('each kind of advice says what is wrong in a sentence an operator can act on', () => {
  for (const kind of ['too_small', 'unguaranteed', 'too_large'] as const) {
    const words = adviceWords(kind);
    assert.ok(words.label.length > 0 && words.hint.endsWith('.'), kind);
  }
});

test('a pin is compared the way the server compares it: repository without regard to case', () => {
  assert.equal(
    pinKey({ repo: 'Acme/API', workflow: 'ci.yml', job_name: 'build' }),
    pinKey({ repo: 'acme/api', workflow: 'ci.yml', job_name: 'build' }),
  );
  assert.notEqual(pinKey({ repo: 'acme/api' }), pinKey({ repo: 'acme/api', job_name: 'build' }));
  assert.equal(pinScope({ repo: 'acme/api' }), 'acme/api · every job');
  assert.equal(
    pinScope({ repo: 'acme/api', workflow: 'ci.yml', job_name: 'build' }),
    'acme/api · ci.yml · build',
  );
});
