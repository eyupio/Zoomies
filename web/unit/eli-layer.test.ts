import assert from 'node:assert/strict';
import { readFileSync } from 'node:fs';
import { join } from 'node:path';
import test from 'node:test';

const src = join(import.meta.dirname, '..', 'src');
const tokens = readFileSync(join(src, 'lib/styles/tokens.css'), 'utf8');
const widget = readFileSync(join(src, 'lib/assistant/EliWidget.svelte'), 'utf8');

// Eli is how an operator asks about the thing in front of them, and that is
// often a drawer or a dialog. When it shared the drawer's layer it was painted
// under one, and the inert a drawer puts on the rest of the page made it
// unclickable even where it showed.
test('Eli sits above every other layer', () => {
  const layers = [...tokens.matchAll(/--z-layer-([a-z]+):\s*(\d+)/g)].map(
    ([, name, value]) => [name!, Number(value)] as const,
  );
  const eli = layers.find(([name]) => name === 'assistant');
  assert.ok(eli, 'the assistant layer is defined');
  for (const [name, value] of layers) {
    if (name !== 'assistant') assert.ok(eli[1] > value, `above ${name}`);
  }
  assert.equal([...widget.matchAll(/z-index:\s*var\(--z-layer-assistant\)/g)].length, 2);
});

test('Eli is exempt from the inert an overlay puts on the page', () => {
  assert.match(widget, /class="launcher"[^>]*data-inert-exempt/);
  assert.match(widget, /class="panel"[\s\S]{0,200}data-inert-exempt/);
});

// An "Ask Eli" button carries one problem's context. Appending it to the last
// thread would answer it against whatever was discussed before.
test('an Ask Eli action starts a fresh conversation', () => {
  const store = readFileSync(join(src, 'lib/assistant/eli.svelte.ts'), 'utf8');
  const ask = store.match(/\n {2}ask\(context: EliContext\): void \{[\s\S]*?\n {2}\}/)?.[0] ?? '';
  const clear = ask.indexOf('this.newConversation()');
  assert.ok(clear >= 0, 'ask clears the conversation');
  assert.ok(clear < ask.indexOf('this.pending.push'), 'and does so before queueing its prompt');
});
