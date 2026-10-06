import { test } from 'node:test';
import assert from 'node:assert/strict';
import { hostTarget } from '../src/lib/problems/targets.ts';

// The three OS health codes are about what a host's report says, and the
// report is on the host's own page.
test('the OS health problems open the host itself', () => {
  for (const code of ['host.os_health', 'host.health_stale', 'host.reboot_pending']) {
    assert.deepEqual(hostTarget(code, 'hst_1'), { href: '/hosts/hst_1', label: 'Open the host' });
  }
});

// A host's own page is its OS health page, so it is the wrong place to send
// somebody about the agent going quiet, the host's capacity or a folder that is
// not mounted. Every other host problem keeps the list, which is where the
// host's state and its slots are.
test('every other host problem keeps its link to the list of hosts', () => {
  for (const code of [
    'host.unhealthy',
    'host.work_concentrated',
    'host.shared_folder_unmounted',
    'host.limits_unenforceable',
    undefined,
  ]) {
    assert.deepEqual(hostTarget(code, 'hst_1'), { href: '/hosts', label: 'Open hosts' }, `${code}`);
  }
});
