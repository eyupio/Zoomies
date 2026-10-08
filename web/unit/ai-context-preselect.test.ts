import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  archivedNote,
  installationHint,
  NOT_FOUND_NOTE,
  repositoryIdFromAddress,
} from '../src/lib/aicontext/preselect.ts';

// The address is typed, pasted and edited by anybody, so what it says about a
// repository is trusted only as far as it is GitHub's own kind of number. The
// wizard then looks for that number among the repositories the server listed, and
// ticks nothing else.
test('an address names a repository only by a positive whole number GitHub could have given', () => {
  assert.equal(repositoryIdFromAddress('123456'), 123456);
  assert.equal(repositoryIdFromAddress('1'), 1);
  for (const raw of [
    '',
    'abc',
    '0',
    '-5',
    '1.5',
    '12abc',
    ' 12',
    '1e3',
    '0x10',
    '007',
    // Past the largest integer a number holds exactly, two IDs would be one.
    '9007199254740993',
  ]) {
    assert.equal(repositoryIdFromAddress(raw), undefined, JSON.stringify(raw));
  }
});

test('the installation step says no repository is chosen, unless the person came from one', () => {
  assert.equal(
    installationHint(null),
    'Choose one installation at a time. No repository is preselected.',
  );
  assert.equal(
    installationHint('acme/api'),
    'Choose one installation at a time. acme/api is selected because you came from its page.',
  );
});

// Said instead of nothing, because a person who followed a link from a repository
// and finds none chosen would otherwise wonder what the link was for.
test('a repository that could not be chosen is said so, and why', () => {
  assert.match(NOT_FOUND_NOTE, /not among this installation's repositories/);
  assert.match(NOT_FOUND_NOTE, /none is selected/);
  assert.match(archivedNote('acme/old'), /^acme\/old is archived and cannot be set up/);
  assert.match(archivedNote('acme/old'), /none is selected/);
});
