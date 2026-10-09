import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  isProtectionCheck,
  PROTECTION_CHECKS,
  protectionChecksOn,
  settingsLinks,
} from '../src/lib/kennel/protection.ts';

test('only the four checks that read settings or required checks belong to the tab', () => {
  for (const code of PROTECTION_CHECKS) assert.equal(isProtectionCheck(code), true, code);
  for (const code of ['token.permissions_unset', 'ci.no_timeout', 'guidance.missing', '']) {
    assert.equal(isProtectionCheck(code), false, code);
  }
});

test('the tab is off only when all four of its checks are', () => {
  assert.equal(protectionChecksOn([]), true);
  assert.equal(protectionChecksOn(['ci.no_timeout']), true);
  assert.equal(protectionChecksOn([...PROTECTION_CHECKS].slice(1)), true);
  assert.equal(protectionChecksOn([...PROTECTION_CHECKS]), false);
  assert.equal(protectionChecksOn([...PROTECTION_CHECKS, 'ci.no_timeout']), false);
});

test('each setting finding links to the Actions settings of its own repository', () => {
  for (const code of [
    'token.default_write',
    'exposure.fork_approval_weak',
    'exposure.private_fork_secrets',
  ]) {
    assert.deepEqual(settingsLinks(code, 'https://github.com', 'acme/widgets'), [
      { label: 'Actions settings', href: 'https://github.com/acme/widgets/settings/actions' },
    ]);
  }
});

test('a required check can be fixed in branch protection or in a ruleset, so both are offered', () => {
  assert.deepEqual(
    settingsLinks('protection.required_check_never_reports', 'https://github.com/', 'acme/widgets'),
    [
      { label: 'Branch protection', href: 'https://github.com/acme/widgets/settings/branches' },
      { label: 'Rulesets', href: 'https://github.com/acme/widgets/settings/rules' },
    ],
  );
});

test('an Enterprise host is the one the links go to, with or without a trailing slash', () => {
  for (const base of [
    'https://ghe.example.com',
    'https://ghe.example.com/',
    'https://ghe.example.com//',
  ]) {
    assert.equal(
      settingsLinks('token.default_write', base, 'acme/widgets')[0]?.href,
      'https://ghe.example.com/acme/widgets/settings/actions',
      base,
    );
  }
  // A path prefix on the host is kept, a query and a fragment are not.
  assert.equal(
    settingsLinks('token.default_write', 'https://ghe.example.com/git/?x=1#y', 'acme/widgets')[0]
      ?.href,
    'https://ghe.example.com/git/acme/widgets/settings/actions',
  );
});

test('a repository name that does not look like one makes no link, whatever it says', () => {
  for (const name of [
    '',
    'acme',
    'acme/widgets/extra',
    '../admin/x',
    'acme/..%2f',
    'acme/wid gets',
    'acme/widgets?x=1',
    'acme/widgets#top',
    'javascript:alert(1)/x',
    'acme\\widgets',
  ]) {
    assert.deepEqual(settingsLinks('token.default_write', 'https://github.com', name), [], name);
  }
});

test('an address that is not plain http or https makes no link', () => {
  for (const base of [
    undefined,
    '',
    'github.com',
    'javascript:alert(1)',
    'data:text/html,x',
    'ftp://github.com',
    '//github.com',
    'not a url',
  ]) {
    assert.deepEqual(settingsLinks('token.default_write', base, 'acme/widgets'), [], String(base));
  }
});

test('a check that is not one of the four has no settings page', () => {
  assert.deepEqual(settingsLinks('ci.no_timeout', 'https://github.com', 'acme/widgets'), []);
  assert.deepEqual(settingsLinks('', 'https://github.com', 'acme/widgets'), []);
});
