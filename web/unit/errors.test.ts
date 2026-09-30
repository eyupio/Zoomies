import assert from 'node:assert/strict';
import test from 'node:test';
import { sentence } from '../src/lib/errors.ts';

// The server's refusals are lowercase and unpunctuated because they are also a
// log line. Shown in a toast under a sentence-case title they read as raw
// output, so they are capitalised and stopped -- and only that: not a word of
// them changes, because a 403 names the role required.
test('a server refusal is capitalised and given a full stop without changing a word', () => {
  const cases: [string, string][] = [
    ['your account is not allowed to do that', 'Your account is not allowed to do that.'],
    [
      'no such thing; it may have been removed since the page loaded',
      'No such thing; it may have been removed since the page loaded.',
    ],
    ['  needs the admin role.  ', 'Needs the admin role.'],
    ['Already a sentence!', 'Already a sentence!'],
    ['', ''],
  ];
  for (const [given, want] of cases) assert.equal(sentence(given), want);
});

// A setting key or an ID leading the message is the operator's own word for a
// thing: capitalising `limits.job_stats_window` names a setting that does not
// exist, and the string they would search for is gone.
test('a message that opens with an identifier keeps it exactly as written', () => {
  const cases: [string, string][] = [
    [
      'limits.job_stats_window caps the range at 90 days',
      'limits.job_stats_window caps the range at 90 days.',
    ],
    ['pool_abc123 is still draining', 'pool_abc123 is still draining.'],
  ];
  for (const [given, want] of cases) assert.equal(sentence(given), want);
});

// A message that ends in an address or a path is not a sentence a full stop can
// close: the stop would be copied with the URL.
test('a message that ends in an address or a path gains no full stop', () => {
  for (const given of [
    'the callback must be registered at https://ci.example/oauth/callback',
    'the database is at /var/lib/zoomies/zoomies.db',
    'cannot continue:',
  ]) {
    assert.equal(sentence(given), given.charAt(0).toUpperCase() + given.slice(1));
  }
});
