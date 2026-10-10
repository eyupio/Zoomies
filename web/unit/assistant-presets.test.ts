import { test } from 'node:test';
import assert from 'node:assert/strict';
import {
  answeringProvider,
  DEFAULT_PRESET,
  isSubscriptionKind,
  KIND_LABELS,
  PRESETS,
  presetFor,
} from '../src/lib/settings/assistant.ts';
import type { AssistantProvider } from '../src/lib/api/types.ts';

test('Ollama Cloud, OpenCode Zen and OpenCode Go are presets of the OpenAI-compatible kind, at their addresses', () => {
  const byId = Object.fromEntries(PRESETS.map((p) => [p.id, p]));
  assert.equal(byId['ollama-cloud']?.kind, 'openai_compatible');
  assert.equal(byId['ollama-cloud']?.baseURL, 'https://ollama.com/v1');
  assert.equal(byId['opencode-zen']?.kind, 'openai_compatible');
  assert.equal(byId['opencode-zen']?.baseURL, 'https://opencode.ai/zen/v1');
  assert.equal(byId['opencode-go']?.kind, 'openai_compatible');
  assert.equal(byId['opencode-go']?.baseURL, 'https://opencode.ai/zen/go/v1');
});

test('a new provider starts as Ollama Cloud, and the presets have unique ids', () => {
  assert.equal(DEFAULT_PRESET, 'ollama-cloud');
  assert.ok(PRESETS.some((p) => p.id === DEFAULT_PRESET));
  assert.equal(new Set(PRESETS.map((p) => p.id)).size, PRESETS.length);
  assert.equal(PRESETS[0]?.id, 'ollama-cloud');
  assert.equal(PRESETS[1]?.id, 'opencode-zen');
  assert.equal(PRESETS[2]?.id, 'opencode-go');
});

test('a saved provider opens on the preset it was added as', () => {
  assert.equal(presetFor('openai_compatible', 'https://ollama.com/v1')?.id, 'ollama-cloud');
  assert.equal(presetFor('openai_compatible', 'https://OLLAMA.com/v1/')?.id, 'ollama-cloud');
  assert.equal(presetFor('openai_compatible', 'https://opencode.ai/zen/v1')?.id, 'opencode-zen');
  // Zen's address is the front of Go's, and the two are not mistaken for each other.
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

test('a Claude subscription is a preset of its own kind, with no address and the person’s own plan said', () => {
  const claude = PRESETS.find((p) => p.id === 'claude-code');
  assert.equal(claude?.kind, 'claude_code');
  assert.equal(claude?.baseURL, '');
  assert.equal(claude?.subscription, true);
  assert.match(claude?.help ?? '', /your own/i);
  assert.match(claude?.help ?? '', /yours alone/);
  assert.match(claude?.help ?? '', /Local models only/);
  // Every other preset is shared, and a saved Claude subscription opens on its own preset.
  assert.deepEqual(
    PRESETS.filter((p) => p.subscription).map((p) => p.id),
    ['claude-code', 'codex', 'copilot'],
  );
  assert.equal(presetFor('claude_code', '')?.id, 'claude-code');
  assert.ok(KIND_LABELS.claude_code);
});

const provider = (over: Partial<AssistantProvider>): AssistantProvider =>
  ({ enabled: true, is_default: false, usable: true, ...over }) as AssistantProvider;

test('the provider that answers is the default when it may be used, and otherwise the first that may', () => {
  const mine = provider({ id: 'a', is_default: true });
  const other = provider({ id: 'b' });
  const alices = provider({ id: 'c', is_default: true, usable: false });
  const off = provider({ id: 'd', enabled: false, is_default: true });
  assert.equal(answeringProvider([other, mine])?.id, 'a');
  assert.equal(answeringProvider([alices, other])?.id, 'b');
  assert.equal(answeringProvider([alices])?.id, undefined);
  assert.equal(answeringProvider([off, other])?.id, 'b');
  assert.equal(answeringProvider([])?.id, undefined);
});

test('Codex and Copilot are subscriptions of their own kinds, with no address and no list of models', () => {
  for (const [id, kind, tool, caveat] of [
    ['codex', 'codex', /OpenAI/, /read-only/],
    ['copilot', 'copilot', /GitHub/, /argument/],
  ] as const) {
    const p = PRESETS.find((x) => x.id === id);
    assert.equal(p?.kind, kind);
    assert.equal(p?.baseURL, '');
    assert.equal(p?.subscription, true);
    // They cannot list their models, and start from the tool's own choice.
    assert.ok(!p?.listsModels);
    assert.ok(!p?.defaultModel);
    assert.match(p?.help ?? '', /yours alone/);
    assert.match(p?.help ?? '', /Local models only/);
    assert.match(p?.help ?? '', tool);
    // What each cannot promise is said where it is chosen.
    assert.match(p?.help ?? '', caveat);
    assert.equal(presetFor(kind, '')?.id, id);
    assert.ok(KIND_LABELS[kind]);
  }
  // Only Claude Code can say which models it has.
  assert.deepEqual(
    PRESETS.filter((p) => p.listsModels).map((p) => p.id),
    ['claude-code'],
  );
});

test('exactly the subscription kinds are the ones the page asks nothing of but a name and a model', () => {
  for (const kind of ['claude_code', 'codex', 'copilot'] as const)
    assert.ok(isSubscriptionKind(kind));
  for (const kind of ['fake', 'openai_compatible', 'anthropic', 'openai'] as const)
    assert.ok(!isSubscriptionKind(kind));
});

test('a personal default answers before an installation default', () => {
  const shared = provider({ id: 'shared', is_default: true, owned_by_you: false });
  const own = provider({ id: 'own', is_default: true, owned_by_you: true });
  assert.equal(answeringProvider([shared, own])?.id, 'own');
});
