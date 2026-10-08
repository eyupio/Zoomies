import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readableJobText, unresolvedJobName } from '../src/lib/jobs/name.ts';

test('unexpanded job names and historical timeline text are readable without guessing a project', () => {
  const name = 'Playwright (${{ matrix.projects }})';
  assert.equal(readableJobText(name), 'Playwright (project unavailable)');
  assert.equal(unresolvedJobName(name), true);
  assert.equal(
    readableJobText(`GitHub queued CI / ${name} in eyupio/zoomies, asking for [zoomies-linux-x64]`),
    'GitHub queued CI / Playwright (project unavailable) in eyupio/zoomies, asking for [zoomies-linux-x64]',
  );
});

test('resolved names are preserved and other missing expression values are explicit', () => {
  assert.equal(readableJobText('Playwright (chromium)'), 'Playwright (chromium)');
  assert.equal(unresolvedJobName('Playwright (chromium)'), false);
  assert.equal(
    readableJobText('Test ${{matrix.os}} / ${{ matrix.version }}'),
    'Test value unavailable / value unavailable',
  );
  assert.equal(readableJobText(null), '');
  assert.equal(unresolvedJobName(undefined), false);
});
