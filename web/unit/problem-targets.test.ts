import { test } from 'node:test';
import assert from 'node:assert/strict';
import { controllerTarget, hostTarget } from '../src/lib/problems/targets.ts';

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

// The notice that a newer release exists is about updating, and the Updates
// page reads that same release in full and says what the update mode would do
// about it, so it is where the notice should lead. It carries no target of its
// own, so the code is all there is to go by.
test('the notice of a newer release opens the Updates page', () => {
  assert.deepEqual(controllerTarget('controller.update_available'), {
    href: '/settings/updates',
    label: 'Open Updates',
  });
});

// A controller problem with no page of its own keeps having none: the link is
// for the codes that were given one, and a guess would send somebody to a page
// that says nothing about what they were told.
test('every other controller problem has no link of its own', () => {
  for (const code of [
    'controller.development_update_available',
    'controller.problems_partial',
    'host.unhealthy',
    '',
    undefined,
  ]) {
    assert.equal(controllerTarget(code), null, `${code}`);
  }
});
