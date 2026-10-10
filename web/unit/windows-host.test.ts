import assert from 'node:assert/strict';
import test from 'node:test';
import {
  WINDOWS_HOST_SELECTOR,
  WINDOWS_QUALIFIED,
  defaultHostOS,
  isWindowsProcessHost,
} from '../src/lib/hosts/windows.ts';

for (const [platform, expected] of [
  ['Win32', 'windows'],
  ['Windows', 'windows'],
  ['MacIntel', 'linux'],
  ['macOS', 'linux'],
  ['Darwin', 'linux'],
  ['Linux x86_64', 'linux'],
  ['', 'linux'],
] as const) {
  test(`a browser on "${platform}" starts on the ${expected} command`, () => {
    assert.equal(defaultHostOS(platform), expected);
  });
}

test('the host the Windows agent enrols is recognised', () => {
  assert.equal(isWindowsProcessHost({ os: 'windows', backends: ['process'] }), true);
});

test('a Windows host needs both the OS and the process backend to be recognised', () => {
  assert.equal(isWindowsProcessHost({ os: 'windows', backends: ['docker'] }), false);
  assert.equal(isWindowsProcessHost({ os: 'linux', backends: ['process'] }), false);
  assert.equal(isWindowsProcessHost({ os: 'windows' }), false);
  assert.equal(isWindowsProcessHost(null), false);
  assert.equal(isWindowsProcessHost(undefined), false);
});

test('the pool advice names the selector that reaches a Windows host', () => {
  assert.equal(WINDOWS_HOST_SELECTOR, 'os=windows');
});

// The support matrix decides this, not the dialog. Flipping the constant is the
// whole change that removes the "not yet qualified" line, and this test is the
// reminder that it moves with the matrix.
test('the dialog does not claim Windows is qualified', () => {
  assert.equal(WINDOWS_QUALIFIED, false);
});
