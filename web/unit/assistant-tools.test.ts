import { test } from 'node:test';
import assert from 'node:assert/strict';
import { toolLabel } from '../src/lib/assistant/tools.ts';

test('a tool is called by what it shows, and one nobody named is its own words', () => {
  assert.equal(toolLabel('list_runners'), 'runners');
  assert.equal(toolLabel('get_runner_log'), 'a runner’s log');
  assert.equal(toolLabel('some_new_tool'), 'some new tool');
});

test('the provider tools are named for what a person would call them', () => {
  assert.equal(toolLabel('list_providers'), 'infrastructure providers');
  assert.equal(toolLabel('provider_pairings'), 'which providers serve which pools');
  assert.equal(toolLabel('list_machines'), 'rented machines');
});

test('the catalog is named as what it is, not by its route', () => {
  assert.equal(toolLabel('get_catalog'), 'the catalog of codes and checks');
  assert.equal(toolLabel('get_usage'), 'usage and cost');
});
