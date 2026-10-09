import { test } from 'node:test';
import assert from 'node:assert/strict';
import { poolMatchesSelector, providerMatchesSelector } from '../src/lib/providers/pairing.ts';

// The same table as TestAProviderAndAPoolAgreeOnlyWhenBothSelectorsAllowIt in
// internal/scheduler/providers_test.go: the editor's preview must say what the
// controller will do, or an operator is shown a provider that then buys nothing.
const pool = { name: 'linux-large', backend: 'docker', labels: ['linux', 'x64', 'tier=large'] };
const provider = { name: 'proxmox-lab', kind: 'proxmox', machine_labels: { site: 'garage' } };

test('a provider selector asks for a label the pool carries, bare or as key=value', () => {
  assert.equal(poolMatchesSelector(pool, {}), true);
  assert.equal(poolMatchesSelector(pool, { linux: '' }), true);
  assert.equal(poolMatchesSelector(pool, { tier: 'large' }), true);
  assert.equal(poolMatchesSelector(pool, { name: 'linux-large' }), true);
  assert.equal(poolMatchesSelector(pool, { LINUX: '' }), true);
});

test('a provider selector the pool does not satisfy lets nothing through', () => {
  assert.equal(poolMatchesSelector(pool, { gpu: '' }), false);
  assert.equal(poolMatchesSelector(pool, { tier: 'small' }), false);
  assert.equal(poolMatchesSelector(pool, { linux: '', gpu: '' }), false);
});

test('a pool selector asks for the provider by name, kind or machine label', () => {
  assert.equal(providerMatchesSelector(provider, {}), true);
  assert.equal(providerMatchesSelector(provider, { site: 'garage' }), true);
  assert.equal(providerMatchesSelector(provider, { kind: 'proxmox' }), true);
  assert.equal(providerMatchesSelector(provider, { name: 'proxmox-lab' }), true);
  assert.equal(providerMatchesSelector(provider, { site: 'office' }), false);
  assert.equal(providerMatchesSelector(provider, { rack: '' }), false);
});
