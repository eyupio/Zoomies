import { test } from 'node:test';
import assert from 'node:assert/strict';
import { DEFAULT_PRESET, PRESETS, presetFor } from '../src/lib/settings/assistant.ts';

test('Ollama Cloud and OpenCode Go are presets of the OpenAI-compatible kind, at their addresses', () => {
  const byId = Object.fromEntries(PRESETS.map((p) => [p.id, p]));
  assert.equal(byId['ollama-cloud']?.kind, 'openai_compatible');
  assert.equal(byId['ollama-cloud']?.baseURL, 'https://ollama.com/v1');
  assert.equal(byId['opencode-go']?.kind, 'openai_compatible');
  assert.equal(byId['opencode-go']?.baseURL, 'https://opencode.ai/zen/go/v1');
});

test('a new provider starts as Ollama Cloud, and the presets have unique ids', () => {
  assert.equal(DEFAULT_PRESET, 'ollama-cloud');
  assert.ok(PRESETS.some((p) => p.id === DEFAULT_PRESET));
  assert.equal(new Set(PRESETS.map((p) => p.id)).size, PRESETS.length);
  assert.equal(PRESETS[0]?.id, 'ollama-cloud');
  assert.equal(PRESETS[1]?.id, 'opencode-go');
});

test('a saved provider opens on the preset it was added as', () => {
  assert.equal(presetFor('openai_compatible', 'https://ollama.com/v1')?.id, 'ollama-cloud');
  assert.equal(presetFor('openai_compatible', 'https://OLLAMA.com/v1/')?.id, 'ollama-cloud');
  assert.equal(presetFor('openai_compatible', 'https://opencode.ai/zen/go/v1')?.id, 'opencode-go');
  assert.equal(
    presetFor('openai_compatible', 'http://localhost:11434/v1')?.id,
    'openai-compatible',
  );
  assert.equal(presetFor('anthropic', '')?.id, 'anthropic');
  assert.equal(presetFor('openai', 'https://api.openai.com/v1')?.id, 'openai');
  // The same address under another kind is not the preset.
  assert.notEqual(presetFor('anthropic', 'https://ollama.com/v1')?.id, 'ollama-cloud');
});
