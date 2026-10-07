import assert from 'node:assert/strict';
import { test } from 'node:test';

const { niceValue } = await import('../src/lib/hosts/check-value.ts');

test('a plain whole number of four digits or more gets thousands separators', () => {
  assert.equal(niceValue('1000'), '1,000');
  assert.equal(niceValue('524288'), '524,288');
  assert.equal(niceValue('2097152'), '2,097,152');
});

test('small numbers and anything that is not a plain whole number are left as the agent wrote them', () => {
  for (const v of [
    '',
    '0',
    '999',
    '60',
    '10%',
    '8 GB',
    '6.8.0-60-generic',
    '-4096',
    '+4096',
    '4096.5',
    '0644',
    '1e6',
    ' 4096',
  ]) {
    assert.equal(niceValue(v), v, v);
  }
});

test('a number too large to hold exactly is not rounded into one that was never reported', () => {
  assert.equal(niceValue('9223372036854775807'), '9223372036854775807');
  assert.equal(niceValue('1234567890123456'), '1234567890123456');
  assert.notEqual(niceValue('123456789012345'), '123456789012345');
});
