import { test } from 'node:test';
import assert from 'node:assert/strict';
import { doctorCommand, shellQuote } from '../src/lib/hosts/terminal.ts';
import { suggestedControllerURL } from '../src/lib/addresses.ts';

test('a value a shell leaves alone is not quoted, and any other is one word', () => {
  assert.equal(shellQuote('host_2ctxv4kb6kame'), 'host_2ctxv4kb6kame');
  assert.equal(shellQuote('https://zoomies.example.com:8443'), 'https://zoomies.example.com:8443');
  assert.equal(shellQuote('has space'), "'has space'");
  // An address is whatever an administrator typed into a setting.
  assert.equal(shellQuote('https://x.example/?a=1&b=$(id)'), "'https://x.example/?a=1&b=$(id)'");
  assert.equal(shellQuote("it's"), "'it'\\''s'");
});

test('the doctor command carries the host, the address and, when there is one, the token', () => {
  assert.equal(
    doctorCommand('host_abc', 'https://zoomies.example.com', 'zoo_secret'),
    'zoomies doctor --host host_abc --verbose --url https://zoomies.example.com --token zoo_secret',
  );
  // Authentication off: nothing to send.
  assert.equal(
    doctorCommand('host_abc', 'http://127.0.0.1:8080'),
    'zoomies doctor --host host_abc --verbose --url http://127.0.0.1:8080',
  );
  assert.ok(!doctorCommand('host_abc', 'http://127.0.0.1:8080').includes('--token'));
});

test('a hostile address cannot add a second command', () => {
  const command = doctorCommand('host_abc', 'https://x.example; rm -rf ~', 'zoo_secret');
  assert.ok(command.includes("'https://x.example; rm -rf ~'"), command);
});

test('the address handed out is the configured one unless that is only reachable from here', () => {
  const origin = 'https://zoomies.internal';
  assert.equal(
    suggestedControllerURL('https://zoomies.example.com/', origin),
    'https://zoomies.example.com',
  );
  // The default single-VM install says localhost, which no other machine answers on.
  assert.equal(suggestedControllerURL('http://localhost:8080', origin), origin);
  assert.equal(suggestedControllerURL('http://127.0.0.2:8080', origin), origin);
  assert.equal(suggestedControllerURL('', origin), origin);
  assert.equal(suggestedControllerURL(undefined, origin), origin);
});
