import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  checkPublicAddress,
  classifyExternalURL,
  normaliseAddress,
  sameAddress,
  suggestAddress,
} from '../src/lib/installations/reachability.ts';

// The Connect dialog and the Overview's checklist both ask this, and the
// dialog's answer is the one that matters: an App built against an address
// GitHub cannot reach carries it for ever, because the webhook URL is fixed
// when GitHub creates the App.
test('an address is missing, local to this machine, or one GitHub can be told about', () => {
  for (const missing of ['', '   ', undefined, null]) {
    assert.equal(classifyExternalURL(missing), 'missing', `${JSON.stringify(missing)}`);
  }
  for (const local of [
    // The installer's own single-VM default, which is why this exists.
    'http://localhost:8080',
    'http://127.0.0.1:8080',
    'http://127.0.0.2:8080',
    'http://[::1]:8080',
    'http://zoomies.localhost',
    'HTTP://LOCALHOST:8080',
  ]) {
    assert.equal(classifyExternalURL(local), 'loopback', local);
  }
  for (const reachable of [
    'https://zoomies.example.com',
    'https://203.0.113.9',
    // Private, not loopback: a GitHub Enterprise Server on the same network can
    // deliver here, so this is not the dialog's business to refuse.
    'http://192.168.1.20:8080',
    'http://10.0.0.5',
  ]) {
    assert.equal(classifyExternalURL(reachable), 'reachable', reachable);
  }
});

// A controller cannot be running with an address that does not parse -- the
// validator stops it starting -- and "only this machine can reach it" would be
// the wrong complaint about a typo. This is the same call the dialog made before
// the rule was shared, kept so that sharing it changed no behaviour.
test('an address that does not parse is not called local', () => {
  assert.equal(classifyExternalURL('zoomies.example.com'), 'reachable');
});

// What the dialog saves and what the controller reports after a restart have to
// compare equal, or the form would stay locked behind a change that is already
// in force. The controller trims trailing slashes at startup.
test('a trailing slash and the space around an address do not make it a different one', () => {
  assert.equal(normaliseAddress('  https://zoomies.example.com/  '), 'https://zoomies.example.com');
  assert.ok(sameAddress('https://zoomies.example.com/', ' https://zoomies.example.com'));
  assert.ok(sameAddress('https://example.com/zoomies/', 'https://example.com/zoomies'));
  assert.ok(!sameAddress('https://example.com', 'https://example.com/zoomies'));
  assert.ok(!sameAddress('https://one.example.com', 'https://two.example.com'));
});

// A controller with no address set and an operator who typed nothing are not in
// agreement, and treating them as if they were would unlock the form at once.
test('nothing is never the same address as nothing', () => {
  assert.ok(!sameAddress('', ''));
  assert.ok(!sameAddress(undefined, null));
  assert.ok(!sameAddress('', 'https://zoomies.example.com'));
});

test('a public address is accepted, and saved without its trailing slash', () => {
  for (const [typed, saved] of [
    ['https://zoomies.example.com', 'https://zoomies.example.com'],
    ['  https://zoomies.example.com/  ', 'https://zoomies.example.com'],
    ['http://203.0.113.9:8080', 'http://203.0.113.9:8080'],
    // The controller accepts a path, for a proxy that serves Zoomies under one.
    ['https://example.com/zoomies/', 'https://example.com/zoomies'],
  ] as const) {
    assert.deepEqual(checkPublicAddress(typed), { ok: true, value: saved }, typed);
  }
});

// Each refusal says what to write, because "invalid address" sends an operator
// to guess -- and the dialog's whole purpose is that they do not have to.
test('an address that is not worth saving is refused with what to write instead', () => {
  const refused: [string, RegExp][] = [
    ['', /Give the address/],
    ['   ', /Give the address/],
    ['zoomies.example.com', /full address/],
    ['http://', /full address/],
    ['ftp://zoomies.example.com', /Start it with https/],
    // Parsed as a scheme called "localhost", which is not one GitHub delivers to.
    ['localhost:8080', /Start it with https/],
    ['http://localhost:8080', /only works on this machine/],
    ['http://127.0.0.1:8080', /only works on this machine/],
    ['http://[::1]:8080', /only works on this machine/],
    ['http://zoomies.localhost', /only works on this machine/],
    // Zoomies appends /webhooks/github to the address, so anything that ends in
    // a query or a fragment would swallow it, for ever, inside the App.
    ['https://zoomies.example.com/?x=1', /nothing after a \? or a #/],
    ['https://zoomies.example.com?', /nothing after a \? or a #/],
    ['https://zoomies.example.com/#top', /nothing after a \? or a #/],
  ];
  for (const [typed, message] of refused) {
    const checked = checkPublicAddress(typed);
    assert.equal(checked.ok, false, `${JSON.stringify(typed)} should be refused`);
    if (!checked.ok) assert.match(checked.message, message, typed);
  }
});

// An operator who tunnelled in over SSH is looking at http://localhost:8080,
// which is exactly the address that must not be saved; offering it in the box
// would be offering the mistake they are already stuck on.
test('this browser’s own address is offered only when GitHub could use it', () => {
  assert.equal(suggestAddress('http://localhost:8080'), '');
  assert.equal(suggestAddress('http://127.0.0.1:8080'), '');
  // A page opened from a file reports the origin "null".
  assert.equal(suggestAddress('null'), '');
  assert.equal(suggestAddress('https://zoomies.example.com'), 'https://zoomies.example.com');
  assert.equal(suggestAddress('http://192.168.1.5:8080'), 'http://192.168.1.5:8080');
});
