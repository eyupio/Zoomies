import { test } from 'node:test';
import assert from 'node:assert/strict';
import { settingTitle } from '../src/lib/settings/assistant.ts';

// A toast about a switch names the switch the way the page labels it, as the
// other settings panels do, and falls back to the key only when the row is
// not loaded.
test('a setting toast is titled by the row label, and by the key when there is no row', () => {
  const rows = [{ key: 'assistant.local_only', label: 'Local models only' }];
  assert.equal(settingTitle(rows, 'assistant.local_only'), 'Local models only');
  assert.equal(
    settingTitle(rows, 'assistant.allow_private_provider'),
    'assistant.allow_private_provider',
  );
  assert.equal(settingTitle([], 'assistant.local_only'), 'assistant.local_only');
});
