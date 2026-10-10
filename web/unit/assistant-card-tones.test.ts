import { test } from 'node:test';
import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';

// The six status tones are what an operator learns a runner's state by, and
// the UI rules keep them for that. A provider's badges are facts about a
// provider (its kind, whether it is the default, whether it has a key), so
// they use neutral and accent only.
test('the assistant provider card uses no runner status tone', () => {
  const source = readFileSync(
    new URL('../src/lib/settings/AssistantProviderCard.svelte', import.meta.url),
    'utf8',
  );
  for (const tone of ['idle', 'busy', 'pending', 'draining', 'danger']) {
    assert.ok(!source.includes(`tone="${tone}"`), `the card uses the ${tone} tone`);
  }
});
